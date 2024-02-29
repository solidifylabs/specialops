package specialops

import (
	"bytes"
	"fmt"
	"log"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
)

// mustRunByteCode propagates arguments to runBytecode, calling log.Fatal() on
// error, otherwise returning the result. It's useful for testable examples that
// don't have access to t.Fatal().
func mustRunByteCode(compiled, callData []byte) []byte {
	out, err := runBytecode(compiled, callData)
	if err != nil {
		log.Fatal(err)
	}
	return out
}

func TestRunCompiled(t *testing.T) {
	// hashOrEcho branches based on the first byte of calldata, which indicates
	// whether it should hash (and return) the remaining bytes, or just echo
	// them. It demonstrates JUMPDEST labeling as well as PUSHJUMPDEST(<lbl>) to
	// jump both backwards and forwards in the code.
	hashOrEcho := Code{
		Fn(SUB, CALLDATASIZE, PUSH(1)), // <cds-1> {}
		// A separate Fn() moves us out of "function mode". Note that if we
		// didn't need <cds-1> to stay on the stack we could elide DUP1 and have
		// the result(s) of the last Fn() act as the inputs to this one. The
		// ExpectStackDepth(1) inside a Fn() asserts the incoming "piped" stack
		// size and is equivalent to being placed between the two Fn()s.
		Fn(CALLDATACOPY, PUSH0, PUSH(1), DUP1, ExpectStackDepth(1)), // <cds-1> {cds[1:]}

		Fn(SHR, PUSH(248), Fn(CALLDATALOAD, PUSH0)), // <cds-1, hash?> {cds[1:]}
		Fn(JUMPI, PUSHJUMPDEST("hash")),             // <cds-1> {cds[1:]}

		// Placing the return code here is unnecessarily convoluted, but acts to
		// demonstrate backwards jumping from the end of the hashing code.
		JUMPDEST("return"), // expecting <size> {ret}
		SetStackDepth(1),
		Fn(RETURN, PUSH0),

		JUMPDEST("hash"), // <cds-1> {cds[1:]}
		SetStackDepth(1),
		// Nesting Fn()s provides even greater improvements to readability than
		// chaining does. The next block is equivalent to the more complicated:
		//
		// Fn(KECCAK256, PUSH0)
		// Fn(MSTORE, PUSH0 /*hash already on the stack*/)
		Fn(
			MSTORE, PUSH0, Fn(
				KECCAK256, PUSH0, /*size already on the stack*/
			),
		), // <> {hash}
		PUSH(0x20),               // <32>
		Fn(JUMP, PUSH("return")), // PUSH(string) is syntactic sugar for a PUSHJUMPDEST
	}

	type test struct {
		name     string
		code     Code
		callData []byte
		want     []byte
	}

	tests := []test{
		{
			name: "echo calldata",
			code: Code{
				CALLDATASIZE, PUSH0, PUSH0, CALLDATACOPY,
				CALLDATASIZE, PUSH0, RETURN,
			},
			callData: []byte("hello world"),
			want:     []byte("hello world"),
		},
		{
			name: "KECCAK256 calldata with variety of constant-pushing approaches",
			code: Code{
				CALLDATASIZE,
				PUSH([]byte{0, 0, 0}),    // PUSH3 0x000000
				PUSH(*uint256.NewInt(0)), // PUSH32 0x00…00
				CALLDATACOPY,
				CALLDATASIZE, PUSH(0) /*PUSH1 0x00*/, KECCAK256,
				PUSH0, MSTORE,
				PUSH(0x20), PUSH0, RETURN,
			},
			callData: []byte{0, 1, 2, 3, 4, 5, 6, 7},
			want:     crypto.Keccak256([]byte{0, 1, 2, 3, 4, 5, 6, 7}),
		},
		{
			name:     "conditional echo calldata",
			code:     hashOrEcho,
			callData: []byte{0 /* don't hash*/, 42, 255, 42},
			want:     []byte{42, 255, 42},
		},
		{
			name:     "conditional hash calldata",
			code:     hashOrEcho,
			callData: []byte{1 /*hash*/, 42, 255, 42},
			want:     crypto.Keccak256([]byte{42, 255, 42}),
		},
	}

	// Starting bytecode with `n` PC opcodes results in <0 … n-1> on the stack.
	pcs := make(Code, 20)
	for i := range pcs {
		pcs[i] = opCode(PC)
	}

	// stackTopReturner returns a contract that pushes `depth` PC values to the
	// stack, pulls one of them to the top with `Inverted(toInvert)`, and
	// returns it as a single byte.
	stackTopReturner := func(depth int, toInvert opCode) Code {
		return append(
			append(Code{ /*guarantee fresh memory*/ }, pcs[:depth]...), // <0 … 15>
			Inverted(toInvert),
			Fn(MSTORE, PUSH0),
			Fn(RETURN, PUSH(31), PUSH(1)),
		)
	}

	// DUP with smaller stack returns the nth value.
	for depth := 12; depth < 16; depth++ {
		for i := 0; i < depth; i++ {
			toInvert := DUP1 + opCode(i)
			tests = append(tests, test{
				name: fmt.Sprintf("inverted %v with stack depth %d (<16)", toInvert, depth),
				code: stackTopReturner(depth, toInvert),
				want: []byte{byte(i)},
			})
		}
	}

	// DUP with deeper stack returns a higher value, offset by how much deeper
	// than 16 values the stack is.
	for depth := 16; depth <= len(pcs); depth++ {
		for i := 0; i < 16; i++ {
			toInvert := DUP1 + opCode(i)
			tests = append(tests, test{
				name: fmt.Sprintf("inverted %v with stack depth %d (>=16)", toInvert, depth),
				code: stackTopReturner(depth, toInvert),
				want: []byte{byte(i + depth - 16)},
			})
		}
	}

	// Note that all SWAPs are capped at `depth-1` because of the semantics of
	// counting `Inverted(SWAP1)` from the bottom.

	// SWAP with smaller stack returns the nth value.
	for depth := 12; depth <= 16; depth++ {
		for i := 0; i < depth-1; i++ {
			toInvert := SWAP1 + opCode(i)
			tests = append(tests, test{
				name: fmt.Sprintf("inverted %v with stack depth %d (<16)", toInvert, depth),
				code: stackTopReturner(depth, toInvert),
				want: []byte{byte(i)},
			})
		}
	}

	// SWAP with deeper stack returns a higher value, offset by how much deeper
	// than 16 values the stack is.
	for depth := 16; depth <= len(pcs); depth++ {
		for i := 0; i < 15; i++ {
			toInvert := SWAP1 + opCode(i)
			tests = append(tests, test{
				name: fmt.Sprintf("inverted %v with stack depth %d (>=16)", toInvert, depth),
				code: stackTopReturner(depth, toInvert),
				want: []byte{byte(i + depth - 16)},
			})
		}
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compiled, err := tt.code.Compile()
			if err != nil {
				t.Fatalf("%T.Compile() error %v", tt.code, err)
			}
			t.Logf("Bytecode: %#x", compiled)

			got, err := tt.code.Run(tt.callData)
			if err != nil {
				t.Fatalf("%T.Run(%#x) error %v", tt.code, tt.callData, err)
			}
			if !bytes.Equal(got, tt.want) {
				t.Errorf(
					"%T.Run(%#x) got:\n%#x\n%v\n\nwant:\n%#x\n%v",
					tt.code, tt.callData,
					got, new(uint256.Int).SetBytes(got),
					tt.want, new(uint256.Int).SetBytes(tt.want),
				)
			}
		})
	}
}
