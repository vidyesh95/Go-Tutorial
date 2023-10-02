package main

import "fmt"

/**
* Range
* The range iterates over elements in a variety of data structures.
* The range form of the for loop iterates over a slice or map.
* When ranging over a slice, two values are returned for each iteration.
* The first is the index, and the second is a copy of the element at that index.
*
* range on arrays and slices provides both the index and value for each entry.
* Above we didn’t need the index, so we ignored it with the blank identifier _.
* Sometimes we actually want the indexes though.
* Here we print the indexes and elements in the slice.
*
* range on map iterates over key/value pairs.
* Above we didn’t need the value, so we ignored it with the blank identifier _.
* Sometimes we actually want the keys though.
* Here we print the keys in the map.
* Note that the map is unordered, so their keys are printed in random order.
*
* Range on strings iterates over Unicode code points.
* The first value is the starting byte index of the rune and the second the rune itself.
* In this example we print the byte index of each rune and the rune itself, in decimal and hexadecimal.
 */
func main() {
	fmt.Println("range")
}
