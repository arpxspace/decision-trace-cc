// Measuring and cutting text in terminal cells, as go-runewidth does for
// the Go view: most characters take one cell, wide ones (CJK, emoji) two,
// and combining marks none.

function cells(code: number): number {
  if (
    (code >= 0x0300 && code <= 0x036f) || // combining marks
    (code >= 0x200b && code <= 0x200f) || // zero-width spaces and marks
    (code >= 0xfe00 && code <= 0xfe0f) // variation selectors
  ) {
    return 0
  }
  if (
    (code >= 0x1100 && code <= 0x115f) ||
    (code >= 0x2e80 && code <= 0xa4cf && code !== 0x303f) ||
    (code >= 0xac00 && code <= 0xd7a3) ||
    (code >= 0xf900 && code <= 0xfaff) ||
    (code >= 0xfe30 && code <= 0xfe4f) ||
    (code >= 0xff00 && code <= 0xff60) ||
    (code >= 0xffe0 && code <= 0xffe6) ||
    (code >= 0x1f300 && code <= 0x1faff) ||
    (code >= 0x20000 && code <= 0x3fffd)
  ) {
    return 2
  }
  return 1
}

/** How many cells a character takes. */
export function charWidth(ch: string): number {
  return cells(ch.codePointAt(0) ?? 0)
}

/** How many cells a string takes. */
export function width(s: string): number {
  let w = 0
  for (const ch of s) {
    w += charWidth(ch)
  }
  return w
}

/** Cuts s to at most w cells, ending with tail when it had to cut. */
export function truncate(s: string, w: number, tail = '…'): string {
  if (width(s) <= w) {
    return s
  }
  const room = w - width(tail)
  let out = ''
  let used = 0
  for (const ch of s) {
    const cw = charWidth(ch)
    if (used + cw > room) {
      break
    }
    out += ch
    used += cw
  }
  return out + tail
}

/** Fills lines of width w with whole words, at most max lines. If the text
 * is longer, the last line ends with "…". */
export function wrap(text: string, w: number, max: number): string[] {
  const words = text.split(/\s+/).filter(Boolean)
  if (words.length === 0 || max <= 0) {
    return []
  }
  const lines: string[] = []
  let cur = ''
  for (const word of words) {
    if (cur === '') {
      cur = word
    } else if (width(cur) + 1 + width(word) <= w) {
      cur += ' ' + word
    } else {
      lines.push(cur)
      cur = word
    }
  }
  lines.push(cur)
  const cut = lines.map(l => truncate(l, w))
  if (cut.length > max) {
    const kept = cut.slice(0, max)
    kept[max - 1] = truncate(kept[max - 1] + ' …', w)
    return kept
  }
  return cut
}

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']

const two = (n: number) => String(n).padStart(2, '0')

/** A clock time today, or a date and time on other days: "16:21",
 * "29 Sep 16:21". Times saved as Go's zero time are unknown. */
export function when(at: string | undefined, now = new Date()): string {
  const t = at ? new Date(at) : null
  if (!t || Number.isNaN(t.getTime()) || t.getFullYear() <= 1) {
    return 'time unknown'
  }
  const clock = `${two(t.getHours())}:${two(t.getMinutes())}`
  if (t.toDateString() === now.toDateString()) {
    return clock
  }
  return `${two(t.getDate())} ${MONTHS[t.getMonth()]} ${clock}`
}

/** Who picked an option, as the details panel says it. */
export function pickedBy(by: string | undefined): string {
  switch (by) {
    case 'user':
      return 'Picked by you'
    case 'claude':
      return 'Picked by Claude'
    case 'both':
      return 'Agreed by you and Claude'
  }
  return 'Picked'
}

/** The first 8 characters of a session id, like a short git hash. */
export function short(id: string): string {
  return id.slice(0, 8)
}

export function plural(n: number, word: string): string {
  return n === 1 ? `1 ${word}` : `${n} ${word}s`
}
