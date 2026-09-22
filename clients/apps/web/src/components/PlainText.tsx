/**
 * The single place agent-controlled text becomes DOM. It is rendered as a
 * React text child, so it is escaped; `pre-wrap` preserves formatting. No
 * other component may interpolate agent strings.
 */
export function PlainText({ text, className }: { text: string; className?: string }) {
  return <span className={className ? `plaintext ${className}` : 'plaintext'}>{text}</span>;
}
