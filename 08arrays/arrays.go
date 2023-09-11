package main

import "fmt"

// Arrays in Go
func main() {
	var ages [3]int = [3]int{20, 25, 30}
	var names [4]string = [4]string{"Yash", "Raj", "Rohit", "Sachin"}

	// Print the arrays
	fmt.Println("Print the arrays")
	fmt.Println(ages, len(ages))
	fmt.Println(names, len(names))

	// Print the first element
	fmt.Println("Print the first element")
	fmt.Println(ages[0])
	fmt.Println(names[0])

	// Print the last element
	fmt.Println("Print the last element")
	fmt.Println(ages[len(ages)-1])
	fmt.Println(names[len(names)-1])

	// Print the range
	fmt.Println("Print the range")
	fmt.Println(ages[1:3])
	fmt.Println(names[1:3])

	// Print the range till the end
	fmt.Println("Print the range till the end")
	fmt.Println(ages[1:])
	fmt.Println(names[1:])

	// Print the range from the beginning
	fmt.Println("Print the range from the beginning")
	fmt.Println(ages[:3])
	fmt.Println(names[:3])

	// Print the range from the beginning till the end
	fmt.Println("Print the range from the beginning till the end")
	fmt.Println(ages[:])
	fmt.Println(names[:])

	// Print the range from the beginning till the end
	fmt.Println("Print the range from the beginning till the end")
	fmt.Println(ages)
	fmt.Println(names)
}
