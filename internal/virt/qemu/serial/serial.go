package serial

type Serial struct{ arg string }

func Chardev(id string) Serial { return Serial{arg: "chardev:" + id} }
func (s Serial) Arg() string   { return s.arg }
