/**
 * Internal references, as links. "section 5.4", "sections 4, 6 and 12" and
 * "Appendix A" in a paper's prose become links to those headings, for the
 * reader pages (build-whitepapers.mjs) and the typeset PDFs
 * (typeset-whitepapers.mjs) alike. Anchors are GitHub's heading ids, which
 * the reader pages use and pandoc's gfm reader generates. The references
 * list is left as written, and a reference to a section the paper does not
 * have fails the build.
 */

/** GitHub's heading id, as the reader pages compute it. */
export function headingId(text) {
  return text.toLowerCase().trim().replace(/<[^>]+>/g, '').replace(/[^\p{L}\p{N}\s_-]/gu, '').replace(/\s/g, '-');
}

const HEADING = /^(#{1,6})\s+(.+?)\s*#*\s*$/;
const NUMBERED = /^((?:\d+)(?:\.\d+)*)\.?\s+\S/;
const APPENDIX = /^Appendix\s+([A-Z])\b/;
const NUM = String.raw`\d+(?:\.\d+)*`;
const SECTIONS = new RegExp(String.raw`(?<![\w-])(?<!cross[- ])([Ss]ections?)\s+(${NUM})((?:(?:,\s*|,?\s+and\s+|,?\s+or\s+|\s+to\s+|\s*–\s*)${NUM})*)`, 'g');
const MORE = new RegExp(String.raw`(,\s*|,?\s+and\s+|,?\s+or\s+|\s+to\s+|\s*–\s*)(${NUM})`, 'g');
const APPENDIX_REF = /(?<![\w-])(Appendix\s+([A-Z]))(?![\w-])/g;
// Spans the rewrite must not enter: code, links, link definitions' targets and HTML tags.
const SHIELD = /`[^`\n]*`|!?\[[^\]\n]*\]\([^)\n]*\)|!?\[[^\]\n]*\]\[[^\]\n]*\]|<[^>\n]+>/g;

/** Each numbered heading's id, by its number ("5.4", "12") or appendix letter ("A"). */
export function sectionAnchors(markdown) {
  const anchors = new Map();
  let fence = false;
  for (const line of markdown.split('\n')) {
    if (/^\s*(```|~~~)/.test(line)) fence = !fence;
    const h = !fence && HEADING.exec(line);
    if (!h) continue;
    const text = h[2];
    const n = NUMBERED.exec(text);
    if (n && !anchors.has(n[1])) anchors.set(n[1], headingId(text));
    const a = APPENDIX.exec(text);
    if (a && !anchors.has(a[1])) anchors.set(a[1], headingId(text));
  }
  return anchors;
}

/** The paper's markdown with its internal references linked. */
export function linkSections(markdown, name = 'paper') {
  const anchors = sectionAnchors(markdown);
  const target = (key, said) => {
    const id = anchors.get(key);
    if (!id) throw new Error(`${name}: "${said}" names no section of this paper`);
    return `#${id}`;
  };
  let fence = false;
  let references = false;
  return markdown.split('\n').map((line) => {
    if (/^\s*(```|~~~)/.test(line)) fence = !fence;
    if (fence) return line;
    const h = HEADING.exec(line);
    if (h) {
      if (h[1].length <= 2) references = /^(References|Data credits)$/i.test(h[2]);
      return line;
    }
    if (references || /^\[[^\]]+\]:\s/.test(line)) return line;
    const kept = [];
    const open = line.replace(SHIELD, (span) => `\u0000${kept.push(span) - 1}\u0001`);
    const linked = open
      .replace(SECTIONS, (all, word, first, rest) => {
        const more = rest.replace(MORE, (m, sep, num) => `${sep}[${num}](${target(num, `${word} ${num}`)})`);
        return `[${word} ${first}](${target(first, `${word} ${first}`)})${more}`;
      })
      .replace(APPENDIX_REF, (all, said, letter) => `[${said}](${target(letter, said)})`);
    return linked.replace(/\u0000(\d+)\u0001/g, (m, i) => kept[Number(i)]);
  }).join('\n');
}
