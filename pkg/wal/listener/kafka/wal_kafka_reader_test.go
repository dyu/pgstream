// SPDX-License-Identifier: Apache-2.0

package kafka

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xataio/pgstream/internal/kafka"
	kafkamocks "github.com/xataio/pgstream/internal/kafka/mocks"
	"github.com/xataio/pgstream/pkg/wal"
)

func TestReader_Listen(t *testing.T) {
	t.Parallel()

	testMessage := &kafka.Message{
		Key:   []byte("test-key"),
		Value: []byte("test-value"),
	}

	errTest := errors.New("oh noes")

	tests := []struct {
		name          string
		reader        func(doneChan chan struct{}) *kafkamocks.Reader
		processRecord payloadProcessor

		wantErr error
	}{
		{
			name: "ok",
			reader: func(doneChan chan struct{}) *kafkamocks.Reader {
				var once sync.Once
				return &kafkamocks.Reader{
					FetchMessageFn: func(ctx context.Context) (*kafka.Message, error) {
						defer once.Do(func() { doneChan <- struct{}{} })
						return testMessage, nil
					},
				}
			},
			processRecord: func(ctx context.Context, b []byte, cp wal.CommitPosition) error {
				require.Equal(t, "test-value", string(b))
				return nil
			},

			wantErr: context.Canceled,
		},
		{
			name: "error - fetching message",
			reader: func(doneChan chan struct{}) *kafkamocks.Reader {
				var once sync.Once
				return &kafkamocks.Reader{
					FetchMessageFn: func(ctx context.Context) (*kafka.Message, error) {
						defer once.Do(func() { doneChan <- struct{}{} })
						return nil, errTest
					},
				}
			},
			processRecord: func(ctx context.Context, b []byte, cp wal.CommitPosition) error {
				return fmt.Errorf("processRecord: should not be called")
			},

			wantErr: errTest,
		},
		{
			name: "error - processing message",
			reader: func(doneChan chan struct{}) *kafkamocks.Reader {
				var once sync.Once
				return &kafkamocks.Reader{
					FetchMessageFn: func(ctx context.Context) (*kafka.Message, error) {
						defer once.Do(func() { doneChan <- struct{}{} })
						return testMessage, nil
					},
				}
			},
			processRecord: func(ctx context.Context, b []byte, cp wal.CommitPosition) error {
				return errTest
			},

			wantErr: context.Canceled,
		},
		{
			name: "error - processing message context canceled",
			reader: func(doneChan chan struct{}) *kafkamocks.Reader {
				var once sync.Once
				return &kafkamocks.Reader{
					FetchMessageFn: func(ctx context.Context) (*kafka.Message, error) {
						defer once.Do(func() { doneChan <- struct{}{} })
						return testMessage, nil
					},
				}
			},
			processRecord: func(ctx context.Context, b []byte, cp wal.CommitPosition) error {
				return context.Canceled
			},

			wantErr: context.Canceled,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			doneChan := make(chan struct{}, 1)
			defer close(doneChan)

			r := &Reader{
				reader:        tc.reader(doneChan),
				processRecord: tc.processRecord,
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			wg := sync.WaitGroup{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := r.Listen(ctx)
				require.ErrorIs(t, err, tc.wantErr)
			}()

			for {
				select {
				case <-doneChan:
					cancel()
					wg.Wait()
					return
				case <-ctx.Done():
					return
				}
			}
		})
	}
}

func TestReader_checkpoint(t *testing.T) {
	t.Parallel()

	testMsgs := []*kafka.Message{
		{
			Key:   []byte("test-key-1"),
			Value: []byte("test-value-1"),
		},
		{
			Key:   []byte("test-key-2"),
			Value: []byte("test-value-2"),
		},
	}

	testPositions := []wal.CommitPosition{
		{KafkaPos: testMsgs[0]},
		{KafkaPos: testMsgs[1]},
	}

	errTest := errors.New("oh noes")

	tests := []struct {
		name   string
		reader *kafkamocks.Reader

		wantErr error
	}{
		{
			name: "ok",
			reader: &kafkamocks.Reader{
				CommitMessagesFn: func(ctx context.Context, msgs ...*kafka.Message) error {
					require.ElementsMatch(t, msgs, testMsgs)
					return nil
				},
			},

			wantErr: nil,
		},
		{
			name: "error - committing messages",
			reader: &kafkamocks.Reader{
				CommitMessagesFn: func(ctx context.Context, msgs ...*kafka.Message) error {
					return errTest
				},
			},

			wantErr: errTest,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := Reader{
				reader:                tc.reader,
				backoffMaxElapsedTime: 5 * time.Millisecond,
			}

			err := r.checkpoint(context.Background(), testPositions)
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}
