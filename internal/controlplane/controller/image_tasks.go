package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	taskv1 "github.com/suknna/govirta/pkg/apis/task/v1alpha1"
)

var taskNameSafePattern = regexp.MustCompile(`[^a-z0-9.-]+`)

const taskNameSegmentMax = 24

func cacheImageTask(img imagev1.Image, nodeName string, cacheRoot string) (taskv1.Task, error) {
	input := taskv1.CacheImageInput{
		ImageName:         img.Name,
		ImageUID:          img.UID,
		Version:           img.Spec.Version,
		Format:            string(img.Spec.Format),
		Source:            taskSource(img.Spec.Source),
		DeclaredSizeBytes: img.Spec.DeclaredSizeBytes,
		SHA256:            img.Spec.SHA256,
		CacheRoot:         cacheRoot,
	}
	data, err := json.Marshal(input)
	if err != nil {
		return taskv1.Task{}, fmt.Errorf("controlplane image controller: marshal cache input: %w", err)
	}
	name := imageTaskName("image-cache", img.Name, contentIdentityDigest(taskv1.TaskOperationCacheImageNode, img, nodeName), nodeName)
	return imageTask(img, nodeName, name, deterministicTaskUID(taskv1.TaskOperationCacheImageNode, img, nodeName), taskv1.TaskOperationCacheImageNode, data), nil
}

func deleteCachedImageTask(img imagev1.Image, nodeName string, cacheRoot string) (taskv1.Task, error) {
	input := taskv1.DeleteCachedImageInput{
		ImageName: img.Name,
		ImageUID:  img.UID,
		Version:   img.Spec.Version,
		SHA256:    img.Spec.SHA256,
		CacheRoot: cacheRoot,
	}
	data, err := json.Marshal(input)
	if err != nil {
		return taskv1.Task{}, fmt.Errorf("controlplane image controller: marshal delete input: %w", err)
	}
	name := imageTaskName("image-cache-delete", img.Name, contentIdentityDigest(taskv1.TaskOperationDeleteCachedImageNode, img, nodeName), nodeName)
	return imageTask(img, nodeName, name, deterministicTaskUID(taskv1.TaskOperationDeleteCachedImageNode, img, nodeName), taskv1.TaskOperationDeleteCachedImageNode, data), nil
}

func imageTask(img imagev1.Image, nodeName string, name string, uid string, operation taskv1.TaskOperation, input json.RawMessage) taskv1.Task {
	return taskv1.Task{
		TypeMeta: metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindTask},
		ObjectMeta: metav1.ObjectMeta{
			Name:     name,
			UID:      uid,
			NodeName: nodeName,
		},
		Spec: taskv1.TaskSpec{
			Scope:     taskv1.TaskScopeNode,
			OwnerKind: metav1.KindImage,
			OwnerName: img.Name,
			OwnerUID:  img.UID,
			Operation: operation,
			Input:     input,
		},
		Status: taskv1.TaskStatus{Phase: taskv1.TaskPhasePending},
	}
}

func imageTaskName(prefix, imageName, identity, nodeName string) string {
	return safeTaskSegment(prefix) + "-" + safeTaskSegment(imageName) + "-" + safeTaskSegment(nodeName) + "-" + identity
}

func safeTaskSegment(value string) string {
	lower := strings.ToLower(value)
	replaced := taskNameSafePattern.ReplaceAllString(lower, "-")
	trimmed := strings.Trim(replaced, "-.")
	if trimmed == "" {
		return "x"
	}
	if len(trimmed) > taskNameSegmentMax {
		return trimmed[:taskNameSegmentMax]
	}
	return trimmed
}

func deterministicTaskUID(operation taskv1.TaskOperation, img imagev1.Image, nodeName string) string {
	sum := taskIdentitySum(operation, img, nodeName)
	return "task-" + hex.EncodeToString(sum[:])[:32]
}

func contentIdentityDigest(operation taskv1.TaskOperation, img imagev1.Image, nodeName string) string {
	sum := taskIdentitySum(operation, img, nodeName)
	return hex.EncodeToString(sum[:])[:32]
}

func taskIdentitySum(operation taskv1.TaskOperation, img imagev1.Image, nodeName string) [sha256.Size]byte {
	return sha256.Sum256([]byte(strings.Join([]string{string(operation), img.UID, img.Name, img.Spec.Version, img.Spec.SHA256, string(img.Spec.Format), fmt.Sprintf("%d", img.Spec.DeclaredSizeBytes), string(img.Spec.Source.Type), img.Spec.Source.Location, nodeName}, "\x00")))
}

func taskSource(source imagev1.ImageSource) taskv1.ImageTaskSource {
	return taskv1.ImageTaskSource{Type: taskv1.ImageTaskSourceType(source.Type), Location: source.Location}
}

func cacheTaskMatchesImage(task taskv1.Task, img imagev1.Image) bool {
	if task.Spec.Operation != taskv1.TaskOperationCacheImageNode || task.Spec.OwnerKind != metav1.KindImage || task.Spec.OwnerName != img.Name || task.Spec.OwnerUID != img.UID {
		return false
	}
	var input taskv1.CacheImageInput
	if err := json.Unmarshal(task.Spec.Input, &input); err != nil {
		return false
	}
	return input.ImageName == img.Name && input.ImageUID == img.UID && input.Version == img.Spec.Version && input.SHA256 == img.Spec.SHA256 && input.Format == string(img.Spec.Format) && input.DeclaredSizeBytes == img.Spec.DeclaredSizeBytes && input.Source.Type == taskv1.ImageTaskSourceType(img.Spec.Source.Type) && input.Source.Location == img.Spec.Source.Location
}

func deleteTaskMatchesImage(task taskv1.Task, img imagev1.Image) bool {
	if task.Spec.Operation != taskv1.TaskOperationDeleteCachedImageNode || task.Spec.OwnerKind != metav1.KindImage || task.Spec.OwnerName != img.Name || task.Spec.OwnerUID != img.UID {
		return false
	}
	var input taskv1.DeleteCachedImageInput
	if err := json.Unmarshal(task.Spec.Input, &input); err != nil {
		return false
	}
	return input.ImageName == img.Name && input.ImageUID == img.UID && input.Version == img.Spec.Version && input.SHA256 == img.Spec.SHA256
}
