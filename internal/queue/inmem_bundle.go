package queue

type InMemBundle struct {
	Queue       *InMemJobQueue
	Results     *InMemResultBus
	Attachments *InMemAttachmentStore
	Commands    *InMemCommandBus
	Status      *InMemStatusBus
}

func NewInMemBundle(capacity int, workerCount int, store JobStore) *InMemBundle {
	return &InMemBundle{
		Queue:       NewInMemJobQueue(capacity, store),
		Results:     NewInMemResultBus(capacity),
		Attachments: NewInMemAttachmentStore(),
		Commands:    NewInMemCommandBus(10),
		Status:      NewInMemStatusBus(workerCount * 2),
	}
}

func (b *InMemBundle) Close() error {
	b.Queue.Close()
	b.Results.Close()
	b.Commands.Close()
	b.Status.Close()
	return nil
}
