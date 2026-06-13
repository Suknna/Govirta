package controllers

import "errors"

var (
	errTaskInvalidInput = errors.New("task invalid input")
	errTaskTransientIO  = errors.New("task transient io")
)
