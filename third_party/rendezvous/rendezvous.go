// Copyright 2014 Tyson Tate, Google LLC
// SPDX-License-Identifier: MIT
// See the LICENSE file in this directory for the full license text.

// Package rendezvous implements rendezvous hashing (a.k.a. highest random
// weight hashing). See http://en.wikipedia.org/wiki/Rendezvous_hashing for
// more information.
package rendezvous

import (
	"bytes"
	"crypto/sha1"
	"sort"
)

type Node []byte
type Key []byte
type Score []byte
type Hasher struct {
	nodes []Node
}

// New returns a new Hash ready for use with the given nodes.
func New(nodes ...string) *Hasher {
	hash := &Hasher{
		nodes: make([]Node, 0, len(nodes)),
	}
	hash.Add(nodes...)
	return hash
}

// Add adds additional nodes to the Hash.
func (h *Hasher) Add(nodes ...string) {
	for _, node := range nodes {
		h.nodes = append(h.nodes, Node(node))
	}
}

// Get returns the node with the highest score for the given key. If this Hash
// has no nodes, an empty string is returned.
func (h *Hasher) Get(key string) string {
	var maxScore Score
	var maxNode Node

	keyBytes := Key(key)
	for _, node := range h.nodes {
		score := hash(node, keyBytes)
		if maxNode == nil || bytes.Compare(score, maxScore) > 0 {
			maxScore = score
			maxNode = node
		}
	}

	return string(maxNode)
}

// GetN returns no more than n nodes for the given key, ordered by descending
// score. GetN is not goroutine-safe.
func (h *Hasher) GetN(n int, key string) []string {
	keyBytes := Key(key)

	scores := make([]Score, len(h.nodes))
	indices := make([]int, len(h.nodes))

	for i, node := range h.nodes {
		scores[i] = hash(node, keyBytes)
		indices[i] = i
	}
	less := func(i, j int) bool {
		return bytes.Compare(scores[indices[i]], scores[indices[j]]) > 0
	}
	sort.Slice(indices, less)

	if n > len(h.nodes) {
		n = len(h.nodes)
	}

	nodes := make([]string, n)
	for i := range nodes {
		nodes[i] = string(h.nodes[indices[i]])
	}
	return nodes
}

func hash(node Node, key Key) Score {
	hasher := sha1.New()
	hasher.Write(node)
	hasher.Write([]byte{'\n'})
	hasher.Write(key)
	return hasher.Sum(nil)
}
