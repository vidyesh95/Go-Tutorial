package main

import "fmt"

// Slices are a key data type in Go, giving a more powerful interface to sequences than arrays.
// Unlike arrays, slices are typed only by the elements they contain (not the number of elements).
// To create an empty slice with non-zero length, use the builtin make.
// Here we make a slice of strings of length 3 (initially zero-valued).
// make allocates a zeroed array and returns a slice that refers to that array:

func main() {
	fmt.Println("Slices in Go")
	var slice []string
	fmt.Println(slice)
	fmt.Printf("Variable is of type: %T \n", slice)
	fmt.Println("uninit:", slice, "isSlice nil:", slice == nil, "isLen of slice 0:", len(slice) == 0, "cap:", cap(slice))
	slice = make([]string, 3)
	fmt.Println("emp:", slice, "len:", len(slice), "cap:", cap(slice))
}
