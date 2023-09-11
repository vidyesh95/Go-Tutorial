# Go-Tutorial

## Resources:

### [Go by Example](https://gobyexample.com/)

Go by Example is a hands-on introduction to Go using annotated example programs.

### To run a Go program on the terminal, you need to:

1.Create a Go file with a .go extension.

2.Write your Go code in the file.

3.Save the file.

4.Open a terminal window and navigate to the directory where the Go file is located.

5.Run the following command:

```
go run filename.go
```

where `filename.go` is the name of the Go file.

For example, if you have a Go file called hello.go with the following code:
```
package main

import "fmt"

func main() {
  fmt.Println("Hello, world!")
}
```
You can run the program by typing the following command in the terminal:

```
go run hello.go
```

This will compile and run the Go program, and the output will be "Hello, world!".

Here are some additional things to keep in mind when running Go programs on the terminal:

- If the Go file is located in a different directory, you can use the ```cd``` command to navigate to that directory before running the program.
- If the Go file has any dependencies, the Go compiler will automatically download and install them.
- You can also use the ```go build``` command to compile the Go program into an executable file. This file can then be run without using the ```go run``` command.
