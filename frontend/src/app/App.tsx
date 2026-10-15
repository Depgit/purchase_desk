// The list of screens and their addresses.
import type { ReactNode } from 'react'
import { BrowserRouter, Navigate, Route, Routes, useParams } from 'react-router-dom'
import { ActivityLogPage } from '../activitylog/ActivityLogPage'
import { ApprovalsPage } from '../approvals/ApprovalsPage'
import { LoginPage } from '../auth/LoginPage'
import { SessionProvider, useSession } from '../auth/session'
import { BillTypesPage } from '../billtypes/BillTypesPage'
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
import { VendorPortal } from '../vendorportal/VendorPortal'
import { ZohoBooksPage } from '../zohobooks/ZohoBooksPage'
import { CorporatePage } from '../corporate/CorporatePage'
import { NewCorporatePage } from '../corporate/NewCorporatePage'
import { CorporateSetupPage } from '../corporate/CorporateSetupPage'
import { WarehousePage } from '../warehouse/WarehousePage'
import { VendorSheetsPage } from '../vendorsheets/VendorSheetsPage'
import { ReportsPage } from '../reports/ReportsPage'
import { NewVersionBanner } from '../appversion/NewVersionBanner'
import { MasterApprovalsPage } from '../masterapproval/MasterApprovalsPage'
import { ScreenGate } from '../screenaccess/screen'

function Screens() {
  const { me, ready } = useSession()
  if (!ready) return null
  if (!me) return <LoginPage />
  return (
    <Shell>
      <Routes>
        <Route path="/" element={<DashboardPage />} />
        <Route path="/approvals" element={<ScreenGate screen="approvals"><ApprovalsPage /></ScreenGate>} />
        <Route path="/requests" element={<ScreenGate screen="requests"><RequestsPage /></ScreenGate>} />
        <Route path="/requests/new" element={<ScreenGate screen="requests"><NewRequestPage /></ScreenGate>} />
        <Route path="/requests/:id/edit" element={<ScreenGate screen="requests"><NewRequestPage /></ScreenGate>} />
        <Route path="/documents/:id" element={<DocumentPage />} />
        <Route path="/purchase-orders" element={<ScreenGate screen="purchase_orders"><PurchaseOrdersPage /></ScreenGate>} />
        <Route path="/goods-receipts" element={<ScreenGate screen="goods_receipts"><GoodsReceiptsPage /></ScreenGate>} />
        <Route path="/invoices" element={<ScreenGate screen="invoices"><InvoicesPage /></ScreenGate>} />
        <Route path="/invoices/:id" element={<InvoicePage />} />
        <Route path="/payments" element={<ScreenGate screen="payments"><PaymentsPage /></ScreenGate>} />
        <Route path="/bills/new/:section" element={<BillsGate><NewBillPage /></BillsGate>} />
        <Route path="/bills/:section" element={<Navigate to="/requests" replace />} />
        <Route path="/vendors" element={<ScreenGate screen="vendors"><VendorsPage /></ScreenGate>} />
        <Route path="/vendors/new" element={<ScreenGate screen="vendors"><VendorForm /></ScreenGate>} />
        <Route path="/vendors/:id/edit" element={<ScreenGate screen="vendors"><VendorForm /></ScreenGate>} />
        <Route path="/items" element={<ScreenGate screen="items"><ItemsPage /></ScreenGate>} />
        <Route path="/vendor-mapping" element={<ScreenGate screen="vendor_mapping"><VendorMappingPage /></ScreenGate>} />
        <Route path="/settings/properties" element={<ScreenGate screen="properties"><PropertiesPage /></ScreenGate>} />
        <Route path="/settings/cities" element={<ScreenGate screen="cities"><CitiesClustersPage /></ScreenGate>} />
        <Route path="/settings/budgets" element={<ScreenGate screen="budgets"><BudgetsPage /></ScreenGate>} />
        <Route path="/settings/bill-types" element={<ScreenGate screen="bill_types"><BillTypesPage /></ScreenGate>} />
        <Route path="/settings/item-setup" element={<ScreenGate screen="item_setup"><ItemSetupPage /></ScreenGate>} />
        <Route path="/settings/master-approvals" element={<ScreenGate screen="master_approvals"><MasterApprovalsPage /></ScreenGate>} />
        <Route path="/settings/users" element={<ScreenGate screen="users"><UsersPage /></ScreenGate>} />
        <Route path="/settings/flow" element={<ScreenGate screen="flow"><FlowSettingsPage /></ScreenGate>} />
        <Route path="/settings/templates" element={<ScreenGate screen="templates"><TemplatesPage /></ScreenGate>} />
        <Route path="/settings/zoho" element={<ScreenGate screen="zoho"><ZohoBooksPage /></ScreenGate>} />
        <Route path="/settings/activity" element={<ScreenGate screen="activity"><ActivityLogPage /></ScreenGate>} />
        <Route path="/corporate" element={<ScreenGate screen="corporate"><CorporatePage /></ScreenGate>} />
        <Route path="/corporate/new/:kind" element={<ScreenGate screen="corporate"><NewCorporatePage /></ScreenGate>} />
        <Route path="/settings/corporate" element={<ScreenGate screen="corporate_setup"><CorporateSetupPage /></ScreenGate>} />
        <Route path="/warehouse" element={<ScreenGate screen="warehouse"><WarehousePage /></ScreenGate>} />
        <Route path="/vendor-sheets" element={<ScreenGate screen="vendor_sheets"><VendorSheetsPage /></ScreenGate>} />
        <Route path="/reports" element={<ScreenGate screen="reports"><ReportsPage /></ScreenGate>} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </Shell>
  )
}

export function App() {
  return (
    <BrowserRouter>
      <ToastProvider>
        <NewVersionBanner />
        <Routes>
          {/* Vendors sign in to their own portal; it never loads the staff session. */}
          <Route path="/vendor/*" element={<VendorPortal />} />
          <Route path="*" element={<SessionProvider><Screens /></SessionProvider>} />
        </Routes>
      </ToastProvider>
    </BrowserRouter>
  )
}

// Utility and AMC bills are two screens on one route; the section in the address says which.
function BillsGate({ children }: { children: ReactNode }) {
  const { section } = useParams()
  return <ScreenGate screen={section === 'amc' ? 'bills_amc' : 'bills_utility'}>{children}</ScreenGate>
}
