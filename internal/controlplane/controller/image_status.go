package controller

import (
	"fmt"
	"sort"

	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	taskv1 "github.com/suknna/govirta/pkg/apis/task/v1alpha1"
)

func aggregateImageStatus(img imagev1.Image, nodes []string, tasks map[string]taskv1.Task) imagev1.ImageStatus {
	caches := make([]imagev1.NodeCacheStatus, 0, len(nodes))
	readyCount := 0
	var failedMessage string
	for _, nodeName := range nodes {
		task := tasks[nodeName]
		cache := imagev1.NodeCacheStatus{NodeName: nodeName, Phase: imagev1.ImageCachePhasePending, TaskRef: imagev1.TaskRef{Name: task.Name, UID: task.UID}}
		switch task.Status.Phase {
		case taskv1.TaskPhaseRunning:
			cache.Phase = imagev1.ImageCachePhaseCaching
		case taskv1.TaskPhaseSucceeded:
			observed, err := taskv1.DecodeCacheImageObserved(task.Status.Observed)
			if err == nil && observedMatchesImage(observed, img, nodeName) {
				cache.Phase = imagev1.ImageCachePhaseReady
				cache.CachedPath = observed.CachedPath
				cache.SizeBytes = observed.SizeBytes
				cache.SHA256 = observed.SHA256
				readyCount++
			} else {
				cache.Phase = imagev1.ImageCachePhaseCaching
			}
		case taskv1.TaskPhaseFailed:
			cache.Phase = imagev1.ImageCachePhaseFailed
			cache.Message = task.Status.Message
			failedMessage = fmt.Sprintf("node %s cache task failed: %s", nodeName, task.Status.Message)
		default:
			cache.Phase = imagev1.ImageCachePhasePending
		}
		caches = append(caches, cache)
	}
	sortNodeCaches(caches)
	if failedMessage != "" {
		return imagev1.ImageStatus{Phase: imagev1.ImagePhaseFailed, NodeCaches: caches, Message: failedMessage}
	}
	if len(nodes) > 0 && readyCount == len(nodes) {
		return imagev1.ImageStatus{Phase: imagev1.ImagePhaseReady, ObservedVersion: img.Spec.Version, ObservedSHA256: img.Spec.SHA256, ObservedSizeBytes: img.Spec.DeclaredSizeBytes, NodeCaches: caches}
	}
	return imagev1.ImageStatus{Phase: imagev1.ImagePhaseCaching, NodeCaches: caches}
}

func deletingImageStatus(img imagev1.Image, nodes []string, tasks map[string]taskv1.Task) (imagev1.ImageStatus, bool) {
	caches := make([]imagev1.NodeCacheStatus, 0, len(nodes))
	succeeded := 0
	for _, nodeName := range nodes {
		task := tasks[nodeName]
		cache := imagev1.NodeCacheStatus{NodeName: nodeName, Phase: imagev1.ImageCachePhaseDeleting, TaskRef: imagev1.TaskRef{Name: task.Name, UID: task.UID}}
		if task.Status.Phase == taskv1.TaskPhaseSucceeded {
			if observed, err := taskv1.DecodeDeleteCachedImageObserved(task.Status.Observed); err == nil && observed.NodeName == nodeName && observed.ImageName == img.Name && observed.Version == img.Spec.Version && observed.Deleted {
				succeeded++
			}
		}
		if task.Status.Phase == taskv1.TaskPhaseFailed {
			cache.Phase = imagev1.ImageCachePhaseFailed
			cache.Message = task.Status.Message
		}
		caches = append(caches, cache)
	}
	sortNodeCaches(caches)
	return imagev1.ImageStatus{Phase: imagev1.ImagePhaseDeleting, NodeCaches: caches}, len(nodes) == 0 || succeeded == len(nodes)
}

func observedMatchesImage(observed taskv1.CacheImageObserved, img imagev1.Image, nodeName string) bool {
	return observed.NodeName == nodeName && observed.ImageName == img.Name && observed.Version == img.Spec.Version && observed.Format == string(img.Spec.Format) && observed.SHA256 == img.Spec.SHA256 && observed.SizeBytes == img.Spec.DeclaredSizeBytes
}

func sortNodeCaches(caches []imagev1.NodeCacheStatus) {
	sort.Slice(caches, func(i, j int) bool { return caches[i].NodeName < caches[j].NodeName })
}
