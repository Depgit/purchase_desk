// Shapes of the main objects the API returns.
export interface Grant {
  role_id: number; code: string; name: string; step: string; rights: string[]
  scope: string; cluster_ids: number[]; property_ids: number[]; category_ids: number[]
}
export interface Me {
  id: number; name: string; email: string; mobile: string; department: string; roles: Grant[]; is_admin: boolean; phase: number
}
export interface Line {
  id: number; sku_id: number; sku_code: string; item_name: string; brand: string; pack_size: string; hsn: string
  sub_category_id: number; sub_category: string; category_id: number; category: string
  qty: number; original_qty: number | null; rate: number; gst_pct: number; estimated: boolean
  dispatched_qty: number | null; received_qty: number; value: number
  description: string; unit: string; expense_head_id: number | null
}
export interface Delivery { partner_id: number; partner: string; driver: string; phone: string; date: string; slot: string; vehicle: string; note: string; version: number }
export interface BudgetFlag {
  sub_category_id: number; amount: number; month_budget: number; month_spent: number; over_month: number
  week_budget: number; week_spent: number; over_week: number; day_budget: number; day_spent: number; week: number; is_fb: boolean
}
export interface Doc {
  id: number; number: string; kind: 'purchase' | 'bill' | 'corp_purchase' | 'corp_bill'; property_id: number; property: string; property_kind: string; property_state: string
  cluster_id: number; cluster: string; city: string; vendor_id: number; vendor: string; vendor_type: string; vendor_msme: boolean; vendor_gst_registered: boolean
  vendor_terms: string; requester_id: number; requester: string; requester_step: string; status: string; route_steps: string[]; step_idx: number
  event_reason: string; event_note: string; routed_reason: string; routed_note: string; cm_approved_at: string | null
  preop_user_id: number | null; preop_user: string; return_reason: string; return_note: string; modified_note: string; modified_by: string; modified_at: string | null
  approved_at: string | null; po_number: string; po_date: string | null; deliver_by: string; payment_terms: string; po_closed_short: boolean
  is_transfer: boolean; dispatched_at: string | null; wait_until: string; wait_note: string
  bill_type_id: number | null; bill_type: string; bill_section: string; period_start: string; months: number; remark: string
  created_at: string; lines: Line[]; total: number
  parent_id: number | null; transfer_received_at: string | null; department_id: number | null; department: string
  corp_l1_user_id: number | null; corp_l1: string; corp_l2_user_id: number | null; corp_l2: string; corp_backup_user_id: number | null; corp_backup: string
  expense_head_id: number | null; expense_head: string; quote_file_id: number | null; delivery: Delivery | null
  // added by the approvals view
  owner: string; owner_name: string; status_label: string; amount: number; over_month: number; over_week: number; flagged: boolean
  budget: BudgetFlag[]; bill_flags: string[]; previous_bill: number | null; age_days: number
}
export interface ListRow {
  id: number; number: string; kind: string; po_number: string; created_at: string; property_id: number; property: string
  cluster_id: number; cluster: string; city: string; category: string; items: string; vendor: string; vendor_msme: boolean; requester: string
  amount: number; status: string; status_label: string; owner: string; modified: boolean; event: boolean; deliver_by: string; payment_terms: string
  is_transfer: boolean; bill_type: string; bill_section: string; period_start: string; months: number; property_kind: string
  po_closed_short: boolean; received_value: number; my_turn: boolean; department: string
}
export interface Invoice {
  tds_amount: number; tds_sections: string; net_payable: number
  id: number; document_id: number; document_number: string; kind: string; po_number: string; grn_number: string; delivery_no: number
  received_at: string | null; vendor_id: number; vendor: string; vendor_msme: boolean; vendor_gst_registered: boolean; payment_terms: string
  property_id: number; property: string; cluster_id: number; city: string; bill_type: string; invoice_number: string; invoice_date: string
  amount: number; gst_pct: number | null; file_id: number | null; status: string; match_remark: string; due_date: string; pay_by: string; msme_limit: string
  zoho_bill_id: string; zoho_status: string; zoho_balance: number | null; zoho_paid: number | null; zoho_error: string; paid_at: string | null; payment_ref: string
  ordered_value: number; received_value: number; difference: number; within_tolerance: boolean; held: string[]; created_at: string
  lines?: { line_id: number; sku_code: string; item: string; pack_size: string; hsn: string; ordered: number; received: number; rate: number; gst_pct: number; value: number }[]
}
export interface Property {
  id: number; code: string; name: string; ezee_code: string; cluster_id: number; cluster: string; city_id: number; city: string; state_code: string
  l1_user_id: number | null; l1_name: string; l2_name: string; kind: string; plan_pct: number; ird_pct: number; banquet_pct: number; active: boolean; inactive_reason: string
}
export interface Option { id: number; name: string }

/** Payment terms, as the server accepts them. "15th of next month": anything dated in October is due on 15 November. */
export const PAYMENT_TERMS = ['Net 15', 'Net 30', 'Net 45', '15th of next month', 'Advance']
