package main

import "fmt"

/*
* For is Go’s only looping construct. Here are three basic types of for loops.
* The most basic type, with a single condition.
* A classic initial/condition/after for loop.
* for without a condition will loop repeatedly until you break out of the loop or return from the enclosing function.
* You can also continue to the next iteration of the loop.
* Loops can also be nested. Note the use of a labeled statement to break out of the nested loops.
* The range form of the for loop iterates over a slice or map.
* When ranging over a slice, two values are returned for each iteration. The first is the index, and the second is a copy of the element at that index.
* You can skip the index or value by assigning to _.
* If you only want the index, you can omit the second variable.
 */
func main() {
	// for loop
	fmt.Println("For loop")
	for i := 1; i <= 10; i++ {
		fmt.Println(i)
	}

	// while loop
	fmt.Println("While loop")
	i := 1
	for i <= 10 {
		fmt.Println(i)
		i++
	}

	// loop with break
	fmt.Println("Loop with break")
	for {
		fmt.Println("Loop with break")
		break
	}

	// continue
	fmt.Println("Continue")
	for i := 1; i <= 10; i++ {
		if i%2 == 0 {
			continue
		}
		fmt.Println(i)
	}

	// nested loop
	fmt.Println("Nested loop")
	for i := 1; i <= 3; i++ {
		for j := 1; j <= 3; j++ {
			fmt.Println(i, j)
		}
	}

	// nested loop with break
	fmt.Println("Nested loop with break")
	for i := 1; i <= 3; i++ {
		for j := 1; j <= 3; j++ {
			fmt.Println(i, j)
			if i == 2 && j == 2 {
				break
			}
		}
	}

	// nested loop with continue
	fmt.Println("Nested loop with continue")
	for i := 1; i <= 3; i++ {
		for j := 1; j <= 3; j++ {
			fmt.Println(i, j)
			if i == 2 && j == 2 {
				continue
			}
		}
	}

	// loop with label
	fmt.Println("Loop with label")
Loop:
	for i := 1; i <= 3; i++ {
		for j := 1; j <= 3; j++ {
			fmt.Println(i, j)
			if i == 2 && j == 2 {
				break Loop
			}
		}
	}

	// loop with label and continue
	fmt.Println("Loop with label and continue")
Loop2:
	for i := 1; i <= 3; i++ {
		for j := 1; j <= 3; j++ {
			fmt.Println(i, j)
			if i == 2 && j == 2 {
				continue Loop2
			}
		}
	}

	s := []int{1, 2, 3}
	// loop with range
	fmt.Println("Loop with range")
	for k, v := range s {
		fmt.Println(k, v)
	}
	// loop with range and blank identifier
	fmt.Println("Loop with range and blank identifier")
	for _, v := range s {
		fmt.Println(v)
	}
	// loop with range and blank identifier
	fmt.Println("Loop with range and blank identifier")
	for k := range s {
		fmt.Println(k)
	}
}
