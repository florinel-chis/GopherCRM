import React, { useMemo } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { Box } from '@mui/material';
import { segmentMentions } from './mentions';

// Engines answer in markdown. The text is rendered client-side — nothing is
// converted on the server and nothing is stored twice — and react-markdown
// builds React elements rather than injecting raw HTML, so a hostile answer
// cannot smuggle markup or scripts into the page.

interface HastNode {
  type: string;
  tagName?: string;
  value?: string;
  children?: HastNode[];
  properties?: Record<string, unknown>;
}

// highlightTerms is a rehype plugin that wraps brand-term matches in <mark>
// elements after the markdown is parsed, so highlighting survives bold text,
// list items and headings instead of being confused by the markup around it.
// The actual matching delegates to segmentMentions, which mirrors the Go
// detector — what is highlighted is exactly what was counted.
const highlightTerms = (terms: string[]) => {
  const cleaned = terms.map((t) => t.trim()).filter((t) => t.length > 0);
  if (cleaned.length === 0) {
    return () => () => {};
  }

  const splitTextNode = (node: HastNode): HastNode[] | null => {
    const value = node.value ?? '';
    const segments = segmentMentions(value, cleaned);
    if (!segments.some((segment) => segment.match)) {
      return null;
    }
    return segments.map((segment) =>
      segment.match
        ? {
            type: 'element',
            tagName: 'mark',
            properties: { dataTestid: 'brand-mention' },
            children: [{ type: 'text', value: segment.text }],
          }
        : { type: 'text', value: segment.text }
    );
  };

  const walk = (node: HastNode): void => {
    if (!node.children) {
      return;
    }
    // Code samples keep their literal text: a highlight inside a fenced block
    // would suggest the engine cited the brand when it only echoed an example.
    if (node.tagName === 'code' || node.tagName === 'pre') {
      return;
    }
    const next: HastNode[] = [];
    for (const child of node.children) {
      if (child.type === 'text') {
        const replaced = splitTextNode(child);
        next.push(...(replaced ?? [child]));
      } else {
        walk(child);
        next.push(child);
      }
    }
    node.children = next;
  };

  return () => (tree: HastNode) => walk(tree);
};

interface AnswerMarkdownProps {
  text: string;
  terms: string[];
}

const AnswerMarkdown: React.FC<AnswerMarkdownProps> = ({ text, terms }) => {
  const rehypePlugins = useMemo(() => [highlightTerms(terms)], [terms]);

  return (
    <Box
      data-testid="answer-transcript"
      sx={{
        typography: 'body2',
        wordBreak: 'break-word',
        '& p': { my: 0.75 },
        '& p:first-of-type': { mt: 0 },
        '& h1, & h2, & h3, & h4': { fontSize: '1rem', fontWeight: 600, mt: 1.5, mb: 0.5 },
        '& ul, & ol': { my: 0.5, pl: 3 },
        '& li': { mb: 0.25 },
        '& table': { borderCollapse: 'collapse', my: 1 },
        '& th, & td': { border: 1, borderColor: 'divider', px: 1, py: 0.5 },
        '& blockquote': {
          borderLeft: 3,
          borderColor: 'divider',
          pl: 1.5,
          ml: 0,
          color: 'text.secondary',
        },
        '& code': {
          fontFamily: 'monospace',
          fontSize: '0.85em',
          backgroundColor: 'action.hover',
          px: 0.5,
          borderRadius: '3px',
        },
        '& pre': { overflowX: 'auto', p: 1, backgroundColor: 'action.hover', borderRadius: 1 },
        '& pre code': { backgroundColor: 'transparent', px: 0 },
        '& mark': { backgroundColor: 'warning.light', px: 0.25, borderRadius: '2px' },
        '& a': { color: 'primary.main' },
      }}
    >
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        rehypePlugins={rehypePlugins}
        components={{
          a: ({ children, href }) => (
            <a href={href} target="_blank" rel="noopener noreferrer">
              {children}
            </a>
          ),
        }}
      >
        {text}
      </ReactMarkdown>
    </Box>
  );
};

export default AnswerMarkdown;
