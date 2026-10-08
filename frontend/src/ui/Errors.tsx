// Shows a list of problems returned by the server.
export function Errors({ list }: { list: string[] }) {
  if (!list.length) return null
  return <ul className="err" role="alert">{list.map((e, i) => <li key={i}>{e}</li>)}</ul>
}
