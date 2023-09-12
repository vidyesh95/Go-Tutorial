package main

import "fmt"

// Arrays in Go
func main() {

	// Print the array of Even numbers
	fmt.Println("Print the array of Even numbers")
	var EvenNum [5]int
	EvenNum[0] = 0
	EvenNum[1] = 2
	EvenNum[2] = 4
	EvenNum[3] = 6
	EvenNum[4] = 8
	fmt.Println(EvenNum)

	// Print the array of Odd numbers
	OddNum := [5]int{1, 3, 5, 7, 9}
	fmt.Println("Print the array of Odd numbers", OddNum)

	var ages [3]int = [3]int{20, 25, 30}
	var names [4]string = [4]string{"Yash", "Raj", "Rohit", "Sachin"}

	// Print the arrays
	fmt.Println("Print the arrays")
	fmt.Println("Print the array of ages:", ages, "of length", len(ages))
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
