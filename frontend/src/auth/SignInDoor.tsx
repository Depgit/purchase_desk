// The choice at the top of both sign-in pages: Saltstayz staff sign in to Purchase Desk, vendors to their own portal.
// The two sign-ins are separate (different accounts, passwords and sessions), so one person can hold both, for
// example a staff member who also looks after a small vendor's portal.
import { Link } from 'react-router-dom'

export function SignInDoor({ on }: { on: 'staff' | 'vendor' }) {
  return (
    <nav className="seg door" aria-label="Who is signing in">
      <Link to="/" className={on === 'staff' ? 'on' : ''} aria-current={on === 'staff' ? 'page' : undefined}>Saltstayz staff</Link>
      <Link to="/vendor" className={on === 'vendor' ? 'on' : ''} aria-current={on === 'vendor' ? 'page' : undefined}>Vendor</Link>
    </nav>
  )
}
