package main

import (
	"testing"
)

func TestRingBuffer(t *testing.T) {
	t.Run("Basic Write and Read", func(t *testing.T) {
		rb := NewRingBuffer(10)
		input := "12345"
		rb.Write([]byte(input))

		if rb.String() != input {
			t.Errorf("Expected %s, got %s", input, rb.String())
		}
	})

	t.Run("Overflow Wrap Around", func(t *testing.T) {
		rb := NewRingBuffer(5)
		// Write 3 bytes
		rb.Write([]byte("123"))
		// Write 3 more (total 6, capacity 5)
		// Should contain "23456" (dropping '1') or similar depending on implementation
		// Implementation logic:
		// 1. [1, 2, 3, _, _] pos=3
		// 2. write "456".
		//    n=3. remaining=2.
		//    copy "45" to [3:]. data=[1,2,3,4,5]
		//    copy "6" to [0:]. data=[6,2,3,4,5]
		//    pos = 3 - 2 = 1.
		//    full = true.
		// Reconstruction: data[1:] + data[:1] -> [2,3,4,5] + [6] = "23456"
		rb.Write([]byte("456"))

		expected := "23456"
		if rb.String() != expected {
			t.Errorf("Expected %s, got %s (Internal Data: %v, Pos: %d)", expected, rb.String(), rb.data, rb.pos)
		}
	})

	t.Run("Write Larger Than Buffer", func(t *testing.T) {
		rb := NewRingBuffer(5)
		input := "1234567890"
		// Should keep last 5: "67890"
		rb.Write([]byte(input))

		expected := "67890"
		if rb.String() != expected {
			t.Errorf("Expected %s, got %s", expected, rb.String())
		}
	})
}
