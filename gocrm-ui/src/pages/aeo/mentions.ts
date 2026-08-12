// Brand-mention segmentation shared by the transcript renderer and its tests.
// Matching mirrors the Go detector so what is highlighted is what was counted.

const isWordRune = (rune: string | undefined): boolean =>
  rune !== undefined && /[\p{L}\p{N}_]/u.test(rune);

export interface Segment {
  text: string;
  match: boolean;
}

// Splits `text` into plain and matched segments for every brand term. Used to
// mark the brand up inside an answer transcript; matching mirrors the Go
// detector so what is highlighted is what was counted.
export const segmentMentions = (text: string, terms: string[]): Segment[] => {
  const cleaned = terms.map((term) => term.trim()).filter((term) => term.length > 0);
  if (!text || cleaned.length === 0) {
    return [{ text, match: false }];
  }

  const haystack = text.toLowerCase();
  const hits: Array<{ start: number; end: number }> = [];

  for (const term of cleaned) {
    const needle = term.toLowerCase();
    let from = 0;
    for (;;) {
      const at = haystack.indexOf(needle, from);
      if (at === -1) {
        break;
      }
      const end = at + needle.length;
      if (!isWordRune(text[at - 1]) && !isWordRune(text[end])) {
        hits.push({ start: at, end });
      }
      from = at + 1;
    }
  }

  if (hits.length === 0) {
    return [{ text, match: false }];
  }

  hits.sort((a, b) => a.start - b.start || b.end - a.end);

  const segments: Segment[] = [];
  let cursor = 0;
  for (const hit of hits) {
    if (hit.start < cursor) {
      continue; // overlapping alias, the longer earlier match already covers it
    }
    if (hit.start > cursor) {
      segments.push({ text: text.slice(cursor, hit.start), match: false });
    }
    segments.push({ text: text.slice(hit.start, hit.end), match: true });
    cursor = hit.end;
  }
  if (cursor < text.length) {
    segments.push({ text: text.slice(cursor), match: false });
  }
  return segments;
};
