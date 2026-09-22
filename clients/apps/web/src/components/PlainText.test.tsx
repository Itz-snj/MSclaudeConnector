import { describe, expect, it } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import { PlainText } from './PlainText';

describe('PlainText', () => {
  it('renders agent text as escaped text, never as HTML', () => {
    const markup = renderToStaticMarkup(<PlainText text={'<img src=x onerror="alert(1)">'} />);
    expect(markup).not.toContain('<img');
    expect(markup).toContain('&lt;img');
  });

  it('preserves whitespace via pre-wrap styling', () => {
    const markup = renderToStaticMarkup(<PlainText text={'a\n  b'} />);
    expect(markup).toContain('class="plaintext"');
  });
});
