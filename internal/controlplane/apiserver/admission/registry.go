package admission

// PreApplyChain returns validators that check a caller-submitted apply object
// before apiserver-owned mutation such as finalizer injection, MAC allocation,
// or VM node binding runs.
func PreApplyChain(st StoreReader) Chain {
	return NewChain(
		EnvelopeValidator{},
		SpecValidator{},
		ApplyOperationValidator{},
		VMPowerStateValidator{},
		FieldPolicyValidator{},
		ReferenceValidator{Store: st},
	)
}

// PostApplyChain returns validators that check the final object after
// apiserver-owned mutation and before the object is written to the store.
func PostApplyChain() Chain {
	return NewChain(NICFinalMACValidator{})
}

// DeleteChain returns validators for DELETE requests before the handler stamps
// deletionTimestamp or finalizes an already-deleting object.
func DeleteChain(st StoreReader) Chain {
	return NewChain(ReverseReferenceValidator{Store: st})
}

// StatusPatchChain returns validators for bare status subresource patches before
// the handler merges status into the stored object.
func StatusPatchChain(st StoreReader) Chain {
	return NewChain(
		PatchShapeValidator{},
		StatusTypeValidator{},
		TargetObjectValidator{Store: st},
	)
}

// FinalizersPatchChain returns validators for finalizer removal requests before
// the handler mutates metadata.finalizers.
func FinalizersPatchChain() Chain {
	return NewChain(
		FinalizersPatchShapeValidator{},
		WhitelistFinalizerValidator{},
		FinalizerDeletionPreconditionValidator{},
	)
}

func FinalizersPatchBodyChain() Chain {
	return NewChain(
		FinalizersPatchShapeValidator{},
		WhitelistFinalizerValidator{},
	)
}

func FinalizersPatchTargetChain() Chain {
	return NewChain(FinalizerDeletionPreconditionValidator{})
}
