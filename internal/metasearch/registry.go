package metasearch

// Registry holds the configured providers and exposes them filtered by
// capability. A source surfaces under a capability only when it both advertises
// that Capability AND implements the matching interface — so a dual-capability
// adapter can be held back to cover-only by trimming its Capabilities().
type Registry struct {
	sources []Source
}

// NewRegistry builds a registry over the given sources.
func NewRegistry(sources ...Source) *Registry {
	return &Registry{sources: sources}
}

// CoverSources returns every source that can supply covers.
func (r *Registry) CoverSources() []CoverSource {
	out := make([]CoverSource, 0, len(r.sources))
	for _, s := range r.sources {
		if !HasCapability(s.Capabilities(), CapCover) {
			continue
		}
		if cs, ok := s.(CoverSource); ok {
			out = append(out, cs)
		}
	}

	return out
}

// MetadataSources returns every source that can identify metadata.
func (r *Registry) MetadataSources() []MetadataSource {
	out := make([]MetadataSource, 0, len(r.sources))
	for _, s := range r.sources {
		if !HasCapability(s.Capabilities(), CapIdentify) {
			continue
		}
		if ms, ok := s.(MetadataSource); ok {
			out = append(out, ms)
		}
	}

	return out
}

// MetadataSourceByName returns the identify-capable source with the given name.
func (r *Registry) MetadataSourceByName(name string) (MetadataSource, bool) { //nolint:ireturn // exported API
	for _, ms := range r.MetadataSources() {
		if ms.Name() == name {
			return ms, true
		}
	}

	return nil, false
}
