export interface VerificationPayload {
  password: string;
  action: string;
  object_id: string;
  body_hash: string;
}
