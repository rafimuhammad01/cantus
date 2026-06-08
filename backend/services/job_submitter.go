package services

// JobSubmitter is the interface used by the Generate handler to enqueue pipeline jobs.
// *JobRunner satisfies this interface.
type JobSubmitter interface {
	Submit(videoID string, semitones int) string
}
