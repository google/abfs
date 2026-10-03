// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package fetch

// type Hash = entities.Hash

// type objectDecoder func(ctx context.Context, hash Hash) (binding.ObjectType, binding.Object, error)

// type walkerStreamItem struct {
// 	hash    Hash
// 	objType binding.ObjectType
// 	err     error
// }

// type queueEntry struct {
// 	hash    Hash
// 	objType binding.ObjectType
// 	decode  func() (binding.Object, error)
// }

// type walker struct {
// 	from   Hash
// 	decode objectDecoder

// 	outCh           chan walkerStreamItem
// 	allowObjectType map[binding.ObjectType]bool

// 	lock  sync.Mutex
// 	queue *deque.Deque[queueEntry]
// 	seen  map[Hash]bool
// }

// func newWalker(from Hash, mask remote.ReachableObjectsMask, decoder objectDecoder) *walker {
// 	return &walker{
// 		from:   from,
// 		decode: decoder,
// 		outCh:  make(chan walkerStreamItem),
// 		allowObjectType: map[binding.ObjectType]bool{
// 			gitobjects.ObjectTypeCommit: mask.CommitMask(),
// 			gitobjects.ObjectTypeDir:    mask.TreeMask(),
// 			binding.ObjectTypeBlob:   mask.BlobMask(),
// 		},
// 		queue: new(deque.Deque[queueEntry]),
// 		seen:  make(map[Hash]bool),
// 	}
// }

// func (w *walker) maybeAddQueue(ctx context.Context, link binding.Link) {
// 	w.lock.Lock()
// 	defer w.lock.Unlock()

// 	if w.seen[link.Hash] {
// 		return
// 	}

// 	w.seen[link.Hash] = true
// 	w.queue.PushBack(queueEntry{
// 		hash:    link.Hash,
// 		objType: link.ObjectType,
// 		decode: func() (binding.Object, error) {
// 			_, o, e := w.decode(ctx, link.Hash)

// 			return o, e
// 		},
// 	})
// }

// func (w *walker) handleBatchedEntries(work []queueEntry) error {
// 	ctx := context.Background()

// 	var eg errgroup.Group
// 	for _, entry := range work {
// 		entry := entry

// 		eg.Go(func() error {
// 			w.outCh <- walkerStreamItem{
// 				hash:    entry.hash,
// 				objType: entry.objType,
// 			}

// 			// There aren't any links to traverse for a blob object.
// 			if entry.objType == binding.ObjectTypeBlob {
// 				return nil
// 			}
// 			obj, err := entry.decode()
// 			if err != nil {
// 				return err
// 			}
// 			for _, link := range obj.Links() {
// 				link := link
// 				if !w.allowObjectType[link.ObjectType] {
// 					continue
// 				}

// 				w.maybeAddQueue(ctx, link)
// 			}

// 			return nil
// 		})
// 	}

// 	return eg.Wait()
// }

// // stream returns all reachable objects from configured `from` SHA.
// func (w *walker) stream() (<-chan walkerStreamItem, error) {
// 	ctx := context.Background()

// 	writeError := func(e error) {
// 		w.outCh <- walkerStreamItem{
// 			err: e,
// 		}
// 	}

// 	// Initially decode the first hash to extract the type information,
// 	// and early exit if the hash is bad.
// 	objType, preDecoded, err := w.decode(ctx, w.from)
// 	if err != nil {
// 		return nil, err
// 	}

// 	go func() {
// 		defer close(w.outCh)

// 		w.queue.PushBack(queueEntry{
// 			hash:    w.from,
// 			objType: objType,
// 			decode: func() (binding.Object, error) {
// 				return preDecoded, nil
// 			},
// 		})

// 		for w.queue.Len() > 0 {
// 			workAmount := min(w.queue.Len(), runtime.NumCPU())
// 			work := make([]queueEntry, 0, workAmount)
// 			for i := 0; i < workAmount; i++ {
// 				work = append(work, w.queue.PopFront())
// 			}

// 			if err := w.handleBatchedEntries(work); err != nil {
// 				writeError(err)
// 				return
// 			}
// 		}
// 	}()

// 	return w.outCh, nil
// }

// func min(x, y int) int {
// 	if x < y {
// 		return x
// 	}

// 	return y
// }
