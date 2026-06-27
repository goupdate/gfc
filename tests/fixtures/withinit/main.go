package main

func init() {
	setup()
}

func main() {
	doWork()
}

func setup()  {}
func doWork() { setup() }