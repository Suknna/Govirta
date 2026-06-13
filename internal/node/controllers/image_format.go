package controllers

import (
	"fmt"

	"github.com/suknna/govirta/internal/storage/diskformat"
	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
)

func mapImageFormat(f imagev1.ImageFormat) (diskformat.Format, error) {
	switch f {
	case imagev1.ImageFormatQCOW2:
		return diskformat.FormatQCOW2, nil
	case imagev1.ImageFormatRaw:
		return diskformat.FormatRaw, nil
	default:
		return "", fmt.Errorf("image format %q is unsupported for root volumes", f)
	}
}
