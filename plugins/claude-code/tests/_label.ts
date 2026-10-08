// A state label as a person sees it, on either surface (round 3 review, issue 1). The terminal
// draws one Text, `● word`, in the tone color. The desktop draws a tinted Box holding the dot in
// the tone color and the word in the theme's ink (no `color`): tone, word color and tint tell them apart.
export type Label = { surface: 'terminal' | 'desktop'; tone?: string; wordColor?: string; tint?: string; word: string; dot?: any; wordNode?: any }

function textOf(n: any): string {
  if (typeof n === 'string') return n
  if (!n || typeof n !== 'object') return ''
  if (n.type === 'Button') return String(n.props?.label ?? '')
  return (n.children ?? []).map(textOf).join('')
}

function walk(n: any, fn: (n: any) => boolean): any {
  if (Array.isArray(n)) {
    for (const c of n) {
      const got = walk(c, fn)
      if (got) return got
    }
    return undefined
  }
  if (!n || typeof n !== 'object') return undefined
  if (fn(n)) return n
  return walk(n.children ?? [], fn)
}

// labelIn finds the label `● word` in a drawn tree (a mounted pane's `await p.drawn()`, a Drawn's `.rows`).
export function labelIn(tree: any, word: string): Label | undefined {
  const want = '● ' + word
  const desktop = walk(tree, (n) => n.type === 'Box' && n.props?.backgroundColor && n.children?.length === 2 && textOf(n) === want)
  if (desktop) {
    const [dot, wordNode] = desktop.children
    return { surface: 'desktop', tone: dot.props?.color, wordColor: wordNode.props?.color, tint: desktop.props.backgroundColor, word, dot, wordNode }
  }
  const terminal = walk(tree, (n) => n.type === 'Text' && textOf(n) === want)
  if (terminal) return { surface: 'terminal', tone: terminal.props?.color, wordColor: terminal.props?.color, word }
  return undefined
}

// labelText is the text a surface's words list holds for a label: the terminal's `● word`, the desktop's word alone (its dot is a Text of its own).
export function labelText(surface: string, word: string): string {
  return surface === 'terminal' ? '● ' + word : word
}

// labelsIn is every label in a tree whose word matches.
export function labelsIn(tree: any, match: RegExp): string[] {
  const out: string[] = []
  const visit = (n: any) => {
    if (Array.isArray(n)) return n.forEach(visit)
    if (!n || typeof n !== 'object') return
    const t = textOf(n)
    const desktop = n.type === 'Box' && n.props?.backgroundColor && n.children?.length === 2 && t.startsWith('● ')
    const terminal = n.type === 'Text' && t.startsWith('● ')
    if ((desktop || terminal) && match.test(t.slice(2))) out.push(t.slice(2))
    else visit(n.children ?? [])
  }
  visit(tree)
  return out
}

// tonedIn finds a word drawn by kit.toned: its tone color and the color of the word itself. Where a
// surface draws the dot apart (a low-contrast tone off the terminal) the tone is the dot's and the word has none.
export function tonedIn(tree: any, word: string): { tone?: string; wordColor?: string; dotted: boolean } | undefined {
  const box = walk(tree, (n) => n.type === 'Box' && n.children?.length === 2 && n.children[0]?.type === 'Text' && textOf(n.children[0]) === '● ' && textOf(n.children[1]) === word)
  if (box) return { tone: box.children[0].props?.color, wordColor: box.children[1].props?.color, dotted: true }
  const t = walk(tree, (n) => n.type === 'Text' && textOf(n) === word)
  if (t) return { tone: t.props?.color, wordColor: t.props?.color, dotted: false }
  return undefined
}
