package services

// JobSubmitter is the interface used by the Generate and Prewarm handlers to enqueue pipeline jobs.
// *JobRunner satisfies this interface.
type JobSubmitter interface {
	Submit(videoID string, semitones int) string
	SubmitPrewarm(videoID string) string
}
