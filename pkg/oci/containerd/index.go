package containerd

import (
	"maps"

	"github.com/opencontainers/go-digest"

	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/store"
)

type Index struct {
	tagNames map[string]digest.Digest
	contents map[digest.Digest]any
}

func NewIndex() *Index {
	return &Index{
		tagNames: map[string]digest.Digest{},
		contents: map[digest.Digest]any{},
	}
}

func (idx *Index) AddImage(img oci.Image) []store.Event {
	tagName, ok := img.TagName()
	if !ok {
		return nil
	}
	dgst, ok := idx.tagNames[tagName]
	if dgst != img.Digest {
		idx.tagNames[tagName] = img.Digest
	}
	if ok {
		return nil
	}
	return []store.Event{{Type: store.CreateEvent, Reference: tagName}}
}

func (idx *Index) RemoveImage(img oci.Image) []store.Event {
	tagName, ok := img.TagName()
	if !ok {
		return nil
	}
	if _, ok := idx.tagNames[tagName]; !ok {
		return nil
	}
	delete(idx.tagNames, tagName)
	return []store.Event{{Type: store.DeleteEvent, Reference: tagName}}
}

func (idx *Index) AddContent(dgst digest.Digest) []store.Event {
	if _, ok := idx.contents[dgst]; ok {
		return nil
	}
	idx.contents[dgst] = nil
	return []store.Event{{Type: store.CreateEvent, Digest: dgst}}
}

func (idx *Index) DiffContent(dgsts []digest.Digest) []store.Event {
	events := []store.Event{}
	contents := map[digest.Digest]any{}
	deleted := maps.Clone(idx.contents)

	for _, dgst := range dgsts {
		if _, ok := deleted[dgst]; ok {
			delete(deleted, dgst)
		} else {
			events = append(events, store.Event{Type: store.CreateEvent, Digest: dgst})
		}
		contents[dgst] = nil
	}
	for dgst := range deleted {
		events = append(events, store.Event{Type: store.DeleteEvent, Digest: dgst})
	}

	idx.contents = contents

	return events
}
