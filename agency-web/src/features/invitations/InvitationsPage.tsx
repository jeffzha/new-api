import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { PageHeader } from "../../components/PageHeader";
import { ErrorNotice, Field, Loading } from "../../components/ui";
import { ApiError } from "../../lib/api";
import { useQuery } from "../../lib/query";
import { loadInvitationPNG } from "./png";

interface Invitation {
  display_name: string;
  invite_code: string;
  invite_url: string;
  invite_qr_url: string;
}

export function InvitationsPage() {
  const { t } = useTranslation();
  const query = useQuery<Invitation>("/invitation");
  if (query.loading) return <Loading />;
  if (query.error || !query.data) {
    const unavailable =
      query.error instanceof ApiError && query.error.code === "invitation_url_unavailable";
    return (
      <section aria-label={t("Invite customers")}>
        <PageHeader icon="invitation" title={t("Invite customers")} description={t("Share your registration link or QR code with new customers.")} actions={<button type="button" className="secondary" onClick={query.reload}>{t("Retry")}</button>} />
        <ErrorNotice
          error={
            new Error(
              unavailable
                ? "The platform invitation address is not configured. Contact your administrator."
                : "Your invitation could not be loaded. Refresh and try again.",
            )
          }
        />
      </section>
    );
  }
  return (
    <InvitationDetails
      key={`${query.data.invite_url}:${query.data.invite_qr_url}`}
      invitation={query.data}
    />
  );
}

function InvitationDetails({ invitation }: { invitation: Invitation }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState(false);
  const [copying, setCopying] = useState(false);
  const [qrVersion, setQRVersion] = useState(0);
  const [qr, setQR] = useState<{
    url?: string;
    error?: Error;
    loading: boolean;
  }>({ loading: true });
  const qrURL = invitation.invite_qr_url;
  useEffect(() => {
    const controller = new AbortController();
    let objectURL: string | undefined;
    setQR({ loading: true });
    void loadInvitationPNG(qrURL, controller.signal)
      .then((blob) => {
        if (controller.signal.aborted) return;
        objectURL = URL.createObjectURL(blob);
        setQR({ url: objectURL, loading: false });
      })
      .catch(() => {
        if (!controller.signal.aborted)
          setQR({
            error: new Error("The invitation QR code could not be loaded. Try again."),
            loading: false,
          });
      });
    return () => {
      controller.abort();
      if (objectURL) URL.revokeObjectURL(objectURL);
    };
  }, [qrURL, qrVersion]);

  async function copyLink() {
    setCopied(false);
    setCopyError(false);
    setCopying(true);
    try {
      await navigator.clipboard.writeText(invitation.invite_url);
      setCopied(true);
    } catch {
      setCopyError(true);
    } finally {
      setCopying(false);
    }
  }

  return (
    <section className="invitation-page" aria-label={t("Invite customers")}>
      <PageHeader icon="invitation" title={t("Invite customers")} description={t("Share your registration link or QR code with new customers.")} />
      <dl>
        <dt>{t("Agency name")}</dt>
        <dd>{invitation.display_name}</dd>
        <dt>{t("Invitation code")}</dt>
        <dd>
          <code>{invitation.invite_code}</code>
        </dd>
      </dl>
      <Field
        label={t("Invitation link")}
        hint={t("New customers who register through this link will be assigned to your agency.")}
      >
        <textarea
          className="invitation-link"
          rows={3}
          readOnly
          value={invitation.invite_url}
          spellCheck={false}
        />
      </Field>
      <div className="actions">
        <button type="button" disabled={copying} onClick={() => void copyLink()}>
          {t("Copy invitation link")}
        </button>
        <a href={invitation.invite_url} target="_blank" rel="noopener noreferrer">
          {t("Open registration page")}
        </a>
      </div>
      {copied && <p role="status">{t("Invitation link copied.")}</p>}
      {copyError && (
        <ErrorNotice
          error={new Error("Copy failed. Select and copy the full invitation link above.")}
        />
      )}
      <div className="invitation-qr">
        <h3>{t("Invitation QR code")}</h3>
        {qr.loading && <Loading />}
        <ErrorNotice error={qr.error} />
        {qr.url && (
          <>
            <img
              src={qr.url}
              width={256}
              height={256}
              alt={t("Registration QR code for {{agency}}", {
                agency: invitation.display_name,
              })}
            />
            <div className="actions">
              <a className="button secondary" href={qr.url} download="agency-invitation.png">
                {t("Download QR code (PNG)")}
              </a>
            </div>
          </>
        )}
        {qr.error && (
          <button
            type="button"
            className="secondary"
            onClick={() => setQRVersion((version) => version + 1)}
          >
            {t("Retry QR code")}
          </button>
        )}
      </div>
    </section>
  );
}
