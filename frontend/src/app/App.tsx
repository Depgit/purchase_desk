// The list of screens and their addresses.
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import { ActivityLogPage } from '../activitylog/ActivityLogPage'
import { ApprovalsPage } from '../approvals/ApprovalsPage'
import { LoginPage } from '../auth/LoginPage'
import { SessionProvider, useSession } from '../auth/session'
import { BillTypesPage } from '../billtypes/BillTypesPage'
import { BillsPage } from '../bills/BillsPage'
import { NewBillPage } from '../bills/NewBillPage'
import { BudgetsPage } from '../budgets/BudgetsPage'
import { DashboardPage } from '../dashboard/DashboardPage'
import { DocumentPage } from '../documents/DocumentPage'
import { FlowSettingsPage } from '../flowsettings/FlowSettingsPage'
import { TemplatesPage } from '../flowsettings/TemplatesPage'
import { GoodsReceiptsPage } from '../goodsreceipts/GoodsReceiptsPage'
import { InvoicePage } from '../invoices/InvoicePage'
import { InvoicesPage } from '../invoices/InvoicesPage'
import { ItemSetupPage } from '../items/ItemSetupPage'
import { ItemsPage } from '../items/ItemsPage'
import { Shell } from '../layout/Shell'
import { PaymentsPage } from '../payments/PaymentsPage'
import { CitiesClustersPage } from '../properties/CitiesClustersPage'
import { PropertiesPage } from '../properties/PropertiesPage'
import { PurchaseOrdersPage } from '../purchaseorders/PurchaseOrdersPage'
import { NewRequestPage } from '../requests/NewRequestPage'
import { RequestsPage } from '../requests/RequestsPage'
import { ToastProvider } from '../ui/Toast'
import { UsersPage } from '../users/UsersPage'
import { VendorMappingPage } from '../vendormapping/VendorMappingPage'
import { VendorForm } from '../vendors/VendorForm'
import { VendorsPage } from '../vendors/VendorsPage'
import { ZohoBooksPage } from '../zohobooks/ZohoBooksPage'
import { CorporatePage } from '../corporate/CorporatePage'
import { NewCorporatePage } from '../corporate/NewCorporatePage'
import { CorporateSetupPage } from '../corporate/CorporateSetupPage'
import { WarehousePage } from '../warehouse/WarehousePage'
import { VendorSheetsPage } from '../vendorsheets/VendorSheetsPage'
import { ReportsPage } from '../reports/ReportsPage'
import { MyWorkPage } from '../mywork/MyWorkPage'
import { PHASE, PhaseGate, usePhase } from '../releasephase/phase'

function Screens() {
  const { me, ready } = useSession()
  if (!ready) return null
  if (!me) return <LoginPage />
  return (
    <Shell>
      <Routes>
        <Route path="/" element={<Home />} />
        <Route path="/approvals" element={<ApprovalsPage />} />
        <Route path="/requests" element={<RequestsPage />} />
        <Route path="/requests/new" element={<NewRequestPage />} />
        <Route path="/requests/:id/edit" element={<NewRequestPage />} />
        <Route path="/documents/:id" element={<DocumentPage />} />
        <Route path="/purchase-orders" element={<PurchaseOrdersPage />} />
        <Route path="/goods-receipts" element={<GoodsReceiptsPage />} />
        <Route path="/invoices" element={<InvoicesPage />} />
        <Route path="/invoices/:id" element={<InvoicePage />} />
        <Route path="/payments" element={<PaymentsPage />} />
        <Route path="/bills/new/:section" element={<PhaseGate phase={PHASE.bills}><NewBillPage /></PhaseGate>} />
        <Route path="/bills/:section" element={<PhaseGate phase={PHASE.bills}><BillsPage /></PhaseGate>} />
        <Route path="/vendors" element={<VendorsPage />} />
        <Route path="/vendors/new" element={<VendorForm />} />
        <Route path="/vendors/:id/edit" element={<VendorForm />} />
        <Route path="/items" element={<ItemsPage />} />
        <Route path="/vendor-mapping" element={<VendorMappingPage />} />
        <Route path="/settings/properties" element={<PropertiesPage />} />
        <Route path="/settings/cities" element={<CitiesClustersPage />} />
        <Route path="/settings/budgets" element={<PhaseGate phase={PHASE.budgets}><BudgetsPage /></PhaseGate>} />
        <Route path="/settings/bill-types" element={<PhaseGate phase={PHASE.bills}><BillTypesPage /></PhaseGate>} />
        <Route path="/settings/item-setup" element={<ItemSetupPage />} />
        <Route path="/settings/users" element={<UsersPage />} />
        <Route path="/settings/flow" element={<FlowSettingsPage />} />
        <Route path="/settings/templates" element={<TemplatesPage />} />
        <Route path="/settings/zoho" element={<PhaseGate phase={PHASE.zoho}><ZohoBooksPage /></PhaseGate>} />
        <Route path="/settings/activity" element={<ActivityLogPage />} />
        <Route path="/corporate" element={<PhaseGate phase={PHASE.corporate}><CorporatePage /></PhaseGate>} />
        <Route path="/corporate/new/:kind" element={<PhaseGate phase={PHASE.corporate}><NewCorporatePage /></PhaseGate>} />
        <Route path="/settings/corporate" element={<PhaseGate phase={PHASE.corporate}><CorporateSetupPage /></PhaseGate>} />
        <Route path="/warehouse" element={<PhaseGate phase={PHASE.warehouse}><WarehousePage /></PhaseGate>} />
        <Route path="/vendor-sheets" element={<PhaseGate phase={PHASE.sheets}><VendorSheetsPage /></PhaseGate>} />
        <Route path="/reports" element={<PhaseGate phase={PHASE.reports}><ReportsPage /></PhaseGate>} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </Shell>
  )
}

export function App() {
  return (
    <BrowserRouter>
      <ToastProvider>
        <SessionProvider>
          <Screens />
        </SessionProvider>
      </ToastProvider>
    </BrowserRouter>
  )
}

/** The home page: the role dashboards from phase 5, a simple list of pending work before that. */
function Home() {
  const { on } = usePhase()
  return on(PHASE.dashboard) ? <DashboardPage /> : <MyWorkPage />
}
