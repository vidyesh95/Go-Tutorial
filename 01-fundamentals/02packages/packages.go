package main

import (
	"fmt"
	"math"
	"math/rand"
)

/*
 * Packages
 * A package is a collection of common source code files.
 * Every Go program is made up of packages.
 * Programs start running in package main.
 * This program is using the packages with import paths "fmt" and "math/rand".
 * By convention, the package name is the same as the last element of the import path.
 * For instance, the "math/rand" package comprises files that begin with the statement package rand.
 * A name is exported if it begins with a capital letter. For example, Pi is an exported name, which is exported from the math package.
 */

func main() {
	fmt.Println("Packages in Go")
	fmt.Println("My favorite number is", rand.Intn(10))
	fmt.Println(math.Pi)
}
