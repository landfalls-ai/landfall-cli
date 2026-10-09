// The engine validates every prop of a tree and, on one it does not allow, refuses the WHOLE tree
// ("Text prop "flexShrink" is not allowed; the engine drew its own"). The test element tables
// call this so a disallowed prop fails the test that draws it, not the person's pane. The lists
// are claude-code.d.ts (BoxProps, TextProps, ButtonProps, SvgProps, LinkProps, RasterProps).
const HOVER = ['hover']
const BOX = [
  'key', 'children', 'position', 'top', 'left', 'right', 'bottom', 'flexDirection', 'flexGrow', 'flexShrink', 'flexWrap',
  'alignItems', 'alignSelf', 'justifyContent', 'gap', 'columnGap', 'rowGap', 'width', 'height', 'minWidth', 'minHeight',
  'margin', 'marginX', 'marginY', 'marginTop', 'marginBottom', 'marginLeft', 'marginRight', 'padding', 'paddingX', 'paddingY',
  'paddingTop', 'paddingBottom', 'paddingLeft', 'paddingRight', 'borderStyle', 'borderColor', 'borderDimColor',
  'backgroundColor', 'overflow', 'display', ...HOVER,
]
const TEXT = ['key', 'children', 'color', 'backgroundColor', 'dimColor', 'bold', 'italic', 'underline', 'strikethrough', 'inverse', 'wrap', ...HOVER]
const BUTTON = ['key', 'children', 'label', 'hotkey', 'action', 'plain', 'dimColor', 'variant', 'role', 'autoFocus', 'onPress', ...HOVER]
const ALLOWED: Record<string, string[]> = {
  Box: BOX,
  Text: TEXT,
  Button: BUTTON,
  Svg: ['key', 'children', 'source', 'alt', 'width', 'height', 'isInteractive'],
  Link: ['key', 'children', 'href', 'label'],
  Raster: ['key', 'children', 'columns', 'rows', 'cells'],
}

export function checkProps(type: string, props: Record<string, unknown> | undefined) {
  const allowed = ALLOWED[type]
  if (!allowed || !props) return
  for (const name of Object.keys(props)) {
    if (props[name] === undefined) continue
    if (!allowed.includes(name)) throw new Error(`${type} prop "${name}" is not allowed (the engine would refuse the whole tree)`)
  }
}
