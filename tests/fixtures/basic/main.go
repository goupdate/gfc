package main

func main() {
	helper1()
	h := &Helper{}
	h.method()
	helper2()
}

func helper1() { helper2() }
func helper2() {}

func (h *Helper) method() {}

type Helper struct{}

// unreachable — не вызывается из main, не должен попасть в граф
func unreachable() {}

func unreachable2() {}

func unreachable3() {}