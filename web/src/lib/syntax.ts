// Display-grade highlighting for the two languages this UI shows: the YAML of a
// composed plan and the shell of a worked curl.
//
// Hand-rolled rather than a library because the bundle is the swissd binary --
// highlight.js with two grammars costs more than every page here put together.
// The contract is that the tokens concatenate back to the exact input, so a
// highlighter bug can only mis-colour, never mangle.

export type Kind = "key" | "string" | "number" | "comment" | "punct" | "flag" | "plain";
export interface Token {
  text: string;
  kind: Kind;
}

export function tokenize(src: string, lang: "yaml" | "sh"): Token[][] {
  const line = lang === "yaml" ? yamlLine : shLine;
  return src.split("\n").map(line);
}

function yamlLine(raw: string): Token[] {
  const out: Token[] = [];
  let rest = raw;

  const indent = rest.match(/^\s*/)![0];
  if (indent) out.push({ text: indent, kind: "plain" });
  rest = rest.slice(indent.length);

  if (rest.startsWith("#")) {
    out.push({ text: rest, kind: "comment" });
    return out;
  }

  // A list item can be followed by a key on the same line: "- name: x".
  const dash = rest.match(/^-\s+/);
  if (dash) {
    out.push({ text: dash[0], kind: "punct" });
    rest = rest.slice(dash[0].length);
  } else if (rest === "-") {
    out.push({ text: rest, kind: "punct" });
    return out;
  }

  const key = rest.match(/^("[^"]*"|'[^']*'|[^:#\s][^:#]*?):(\s|$)/);
  if (key) {
    out.push({ text: key[1], kind: "key" });
    out.push({ text: ":", kind: "punct" });
    rest = rest.slice(key[1].length + 1);
  }

  // Trailing comment, kept out of the value so a "# ..." never colours as one.
  let comment = "";
  const hash = rest.search(/(^|\s)#/);
  if (hash >= 0) {
    comment = rest.slice(hash);
    rest = rest.slice(0, hash);
  }
  if (rest) out.push(...yamlValue(rest));
  if (comment) out.push({ text: comment, kind: "comment" });
  return out;
}

function yamlValue(v: string): Token[] {
  const lead = v.match(/^\s*/)![0];
  const body = v.slice(lead.length);
  const toks: Token[] = lead ? [{ text: lead, kind: "plain" }] : [];
  if (!body) return toks;

  if (/^-?\d+(\.\d+)?$/.test(body)) toks.push({ text: body, kind: "number" });
  else if (/^(true|false|null|~)$/i.test(body)) toks.push({ text: body, kind: "number" });
  else if (/^[[{]/.test(body)) toks.push(...flow(body));
  // A scalar is a string whether or not it is quoted, and in YAML it usually
  // is not. Leaving unquoted ones plain left almost every value uncoloured.
  else toks.push({ text: body, kind: "string" });
  return toks;
}

// Inline collections: separators as punctuation, everything between as values.
function flow(body: string): Token[] {
  const out: Token[] = [];
  const re = /("[^"]*"|'[^']*')|(-?\d+(?:\.\d+)?)|([[\]{},:])/g;
  let at = 0;
  for (let m = re.exec(body); m; m = re.exec(body)) {
    // The gaps between separators are the members, and they are scalars too.
    if (m.index > at) out.push({ text: body.slice(at, m.index), kind: "string" });
    out.push({ text: m[0], kind: m[1] ? "string" : m[2] ? "number" : "punct" });
    at = m.index + m[0].length;
  }
  if (at < body.length) out.push({ text: body.slice(at), kind: "string" });
  return out;
}

function shLine(raw: string): Token[] {
  const out: Token[] = [];
  const re = /('[^']*'|"[^"]*")|(\s-{1,2}[A-Za-z][\w-]*)|(\\$)/g;
  let at = 0;
  for (let m = re.exec(raw); m; m = re.exec(raw)) {
    if (m.index > at) out.push({ text: raw.slice(at, m.index), kind: "plain" });
    out.push({ text: m[0], kind: m[1] ? "string" : m[2] ? "flag" : "punct" });
    at = m.index + m[0].length;
  }
  if (at < raw.length) out.push({ text: raw.slice(at), kind: "plain" });
  return out;
}
