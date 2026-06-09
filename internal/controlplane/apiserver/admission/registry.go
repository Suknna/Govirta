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

// FinalizersPatchChain returns the full finalizer-removal validator set in one
// chain. The live handler does NOT use this: it splits validation across the
// store read (body shape/whitelist before the read, deletion precondition after)
// via FinalizersPatchBodyChain + FinalizersPatchTargetChain. This combined chain
// exists only as a single-call convenience for unit tests that supply both the
// body and the target object up front; production code must use the split chains
// so body errors (400) are returned before the store is touched.
func FinalizersPatchChain() Chain {
	return NewChain(
		FinalizersPatchShapeValidator{},
		WhitelistFinalizerValidator{},
		FinalizerDeletionPreconditionValidator{},
	)
}

// FinalizersPatchBodyChain validates only the request body (shape + whitelist),
// with no dependency on the stored target. The handler runs it before reading
// the object so a malformed or non-whitelisted removal is rejected (400/409)
// without a store round-trip.
func FinalizersPatchBodyChain() Chain {
	return NewChain(
		FinalizersPatchShapeValidator{},
		WhitelistFinalizerValidator{},
	)
}

// FinalizersPatchTargetChain validates the precondition that depends on the
// stored target (object must carry a deletionTimestamp). The handler runs it
// after reading the object, separately from the body chain, so the read only
// happens once the body is known valid.
func FinalizersPatchTargetChain() Chain {
	return NewChain(FinalizerDeletionPreconditionValidator{})
}
