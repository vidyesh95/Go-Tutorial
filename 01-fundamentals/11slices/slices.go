package main

import (
	"fmt"
	"slices"
)

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

	slice[0] = "a"
	slice[1] = "b"
	slice[2] = "c"
	fmt.Println("set:", slice)
	fmt.Println("get:", slice[2])

	fmt.Println("len:", len(slice), "cap:", cap(slice))

	slice = append(slice, "d")
	slice = append(slice, "e", "f")
	fmt.Println("appended slice:", slice)

	c := make([]string, len(slice))
	copy(c, slice)
	fmt.Println("copy:", c)

	l := slice[2:5]
	fmt.Println("slice[2:5]:", l)

	l = slice[:5]
	fmt.Println("slice[:5]:", l)

	l = slice[2:]
	fmt.Println("slice[2:]:", l)

	t1 := []string{"g", "h", "i"}
	fmt.Println("dcl1:", t1)

	t2 := []string{"g", "h", "i"}
	fmt.Println("dcl2:", t2)
	if slices.Equal(t1, t2) {
		fmt.Println("t1 and t2 are equal(t1 == t2)")
	} else {
		fmt.Println("t1 and t2 are not equal(t1 != t2)")
	}

	twoD := make([][]int, 3)
	fmt.Println("twoD:", twoD)
	fmt.Println("len(twoD):", len(twoD))
	fmt.Println("cap(twoD):", cap(twoD))
	for i := 0; i < len(twoD); i++ {
		innerLen := i + 1
		twoD[i] = make([]int, innerLen)
		fmt.Println("twoD[i]:", twoD[i])
		fmt.Println("len(twoD[i]):", len(twoD[i]))
		fmt.Println("cap(twoD[i]):", cap(twoD[i]))
		for j := 0; j < innerLen; j++ {
			twoD[i][j] = i + j
			fmt.Println("twoD[i][j]:", twoD[i][j])
		}
	}
	fmt.Println("2d:", twoD)
}
