package main

func main() {
	used()
}

func used() {}

func deadFunc1() {} // nobody calls me — dead

func deadFunc2() {} // nobody calls me — dead

type DeadType struct{}

func (d *DeadType) deadMethod() {} // nobody calls me — dead