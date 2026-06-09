package admission

func PreApplyChain(st StoreReader) Chain {
	return NewChain()
}

func PostApplyChain() Chain {
	return NewChain()
}

func DeleteChain(st StoreReader) Chain {
	return NewChain()
}

func StatusPatchChain() Chain {
	return NewChain()
}

func FinalizersPatchChain() Chain {
	return NewChain()
}
