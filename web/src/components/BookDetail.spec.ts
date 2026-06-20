import { mount, RouterLinkStub } from '@vue/test-utils';
import DOMPurify from 'dompurify';
import { describe, expect, it, vi } from 'vitest';

import BookDetail from '@/components/BookDetail.vue';
import { makeAuthor, makeBook } from '@/test/factories';

// DOMPurify's real stripping is its own (well-tested) job and depends on the host
// DOM parser, which happy-dom doesn't fully satisfy. So we mock it as identity and
// assert the component routes the annotation through it — that wiring is the fix.
vi.mock('dompurify', () => ({
  default: { sanitize: vi.fn((html: string) => html) },
}));

const opts = { global: { stubs: { RouterLink: RouterLinkStub } } };

describe('BookDetail', () => {
  it('renders title, authors, download links and annotation', () => {
    const book = makeBook({
      title: 'Foundation',
      authors: [makeAuthor({ name: 'Isaac Asimov' })],
      annotation: '<p>An epic.</p>',
      formats: [{ type: 'epub', size_bytes: 1048576, download_url: '/api/books/7/file' }],
    });
    const wrapper = mount(BookDetail, { props: { book }, ...opts });

    expect(wrapper.find('[data-testid="detail-title"]').text()).toBe('Foundation');
    expect(wrapper.text()).toContain('Isaac Asimov');

    const download = wrapper.find('[data-testid="download-link"]');
    expect(download.attributes('href')).toBe('/api/books/7/file');
    expect(download.text()).toContain('EPUB');

    expect(wrapper.find('[data-testid="annotation-body"]').html()).toContain('<p>An epic.</p>');
  });

  it('routes the annotation through DOMPurify before v-html (F2)', () => {
    vi.mocked(DOMPurify.sanitize).mockClear();
    const book = makeBook({ annotation: '<p>raw annotation</p>' });
    const wrapper = mount(BookDetail, { props: { book }, ...opts });

    // The sink renders the sanitizer's output, not the raw prop.
    expect(DOMPurify.sanitize).toHaveBeenCalledWith('<p>raw annotation</p>');
    expect(wrapper.find('[data-testid="annotation-body"]').html()).toContain(
      '<p>raw annotation</p>',
    );
  });

  it('shows series with its number as a labeled field', () => {
    const book = makeBook({ series: 'Foundation', series_index: 2 });
    const wrapper = mount(BookDetail, { props: { book }, ...opts });

    const fields = wrapper.find('[data-testid="detail-fields"]');
    expect(fields.text()).toContain('Series');
    expect(fields.text()).toContain('Foundation #2');
  });

  it('omits the series index symbol when series_index is null', () => {
    const book = makeBook({ series: 'Foundation', series_index: null });
    const wrapper = mount(BookDetail, { props: { book }, ...opts });

    // The top subtitle link
    expect(wrapper.text()).toContain('Foundation');
    expect(wrapper.text()).not.toContain('Foundation #');

    // The metadata grid field
    const fields = wrapper.find('[data-testid="detail-fields"]');
    expect(fields.text()).toContain('Foundation');
    expect(fields.text()).not.toContain('Foundation #');
  });

  it('correctly renders series_index when it is 0', () => {
    const book = makeBook({ series: 'Foundation', series_index: 0 });
    const wrapper = mount(BookDetail, { props: { book }, ...opts });

    // The top subtitle link
    expect(wrapper.text()).toContain('Foundation #0');

    // The metadata grid field
    const fields = wrapper.find('[data-testid="detail-fields"]');
    expect(fields.text()).toContain('Foundation #0');
  });
});
