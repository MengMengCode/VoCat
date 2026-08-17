import { useEffect, useState, type ReactElement } from "react";
import { Navigate, Route, Routes, useLocation } from "react-router-dom";
import { AuthProvider, useAuth } from "./store/auth";
import { LanguageProvider } from "./lib/i18n";
import { AuthenticatedShell } from "./components/shell/AuthenticatedShell";
import { UnauthenticatedShell } from "./components/shell/UnauthenticatedShell";
import { Disclaimer } from "./components/Disclaimer";
import { MessageHost } from "./components/ui/message";
import { ConfirmHost } from "./components/ui/MessageBox";
import { LoadingScreen } from "./components/ui/LoadingScreen";
import LoginPage from "./pages/LoginPage";
import DashboardPage from "./pages/DashboardPage";
import DevicesPage from "./pages/DevicesPage";
import ProxyPage from "./pages/ProxyPage";
import ExportProxyPage from "./pages/ExportProxyPage";
import SmsPage from "./pages/SmsPage";
import AutomaticTasksPage from "./pages/AutomaticTasksPage";
import LogsPage from "./pages/LogsPage";
import SettingsPage from "./pages/SettingsPage";
import ExtensionPage from "./pages/ExtensionPage";
import CallPage from "./pages/CallPage";

const THEME_KEY = "theme";
const DISCLAIMER_KEY = "vocat_disclaimer_agreed_at";
const SEVEN_DAYS = 7 * 24 * 60 * 60 * 1000;

function useTheme() {
  const [isDark, setIsDark] = useState<boolean>(() => {
    try {
      return localStorage.getItem(THEME_KEY) === "dark";
    } catch {
      return false;
    }
  });
  useEffect(() => {
    document.documentElement.classList.toggle("dark", isDark);
    try {
      localStorage.setItem(THEME_KEY, isDark ? "dark" : "light");
    } catch {
      /* ignore */
    }
  }, [isDark]);
  return { isDark, toggle: () => setIsDark((value) => !value) };
}

function RequireAuth({ children }: { children: ReactElement }) {
  const { ready, isAuthenticated } = useAuth();
  const location = useLocation();
  if (!ready) return <LoadingScreen />;
  if (!isAuthenticated) {
    const redirect = `${location.pathname}${location.search}${location.hash}`;
    return <Navigate to={`/login?redirect=${encodeURIComponent(redirect)}`} replace />;
  }
  return children;
}

function LoginLayout({ isDark, onToggleTheme }: { isDark: boolean; onToggleTheme: () => void }) {
  const { ready, isAuthenticated } = useAuth();
  if (ready && isAuthenticated) return <Navigate to="/" replace />;
  return <UnauthenticatedShell isDark={isDark} onToggleTheme={onToggleTheme} />;
}

function AppRoot() {
  const { isDark, toggle } = useTheme();
  const { ready, isAuthenticated } = useAuth();
  const [showDisclaimer, setShowDisclaimer] = useState(false);
  const [firstTime, setFirstTime] = useState(true);

  useEffect(() => {
    if (!isAuthenticated) {
      setShowDisclaimer(false);
      return;
    }
    let ts: number | null = null;
    try {
      const raw = localStorage.getItem(DISCLAIMER_KEY);
      ts = raw === null ? null : Number(raw);
    } catch {
      ts = null;
    }
    const expired = ts === null || Number.isNaN(ts) || Date.now() - ts >= SEVEN_DAYS;
    if (expired) {
      setFirstTime(ts === null || Number.isNaN(ts));
      setShowDisclaimer(true);
    }
  }, [isAuthenticated]);

  function agree() {
    try {
      localStorage.setItem(DISCLAIMER_KEY, String(Date.now()));
    } catch {
      /* ignore */
    }
    setShowDisclaimer(false);
  }

  if (!ready) return <LoadingScreen />;

  return (
    <div className="h-screen w-screen overflow-hidden bg-gray-50 font-sans text-gray-900 transition-colors duration-300 selection:bg-indigo-500 selection:text-white dark:bg-[#101014] dark:text-gray-100">
      <Routes>
        <Route path="/login" element={<LoginLayout isDark={isDark} onToggleTheme={toggle} />}>
          <Route index element={<LoginPage />} />
        </Route>
        <Route
          path="/"
          element={
            <RequireAuth>
              <AuthenticatedShell isDark={isDark} onToggleTheme={toggle} />
            </RequireAuth>
          }
        >
          <Route index element={<DashboardPage />} />
          <Route path="devices/*" element={<DevicesPage />} />
          <Route path="proxy" element={<ProxyPage />} />
          <Route path="export-proxy" element={<ExportProxyPage />} />
          <Route path="sms" element={<SmsPage />} />
          <Route path="calls" element={<CallPage />} />
          <Route path="automatic-tasks" element={<AutomaticTasksPage />} />
          <Route path="extensions/:pluginId/:contributionId" element={<ExtensionPage />} />
          <Route path="logs" element={<LogsPage />} />
          <Route path="settings" element={<SettingsPage />} />
        </Route>
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
      {showDisclaimer && <Disclaimer firstTime={firstTime} onAgree={agree} />}
    </div>
  );
}

export default function App() {
  return (
    <AuthProvider>
      <LanguageProvider>
        <MessageHost />
        <ConfirmHost />
        <AppRoot />
      </LanguageProvider>
    </AuthProvider>
  );
}
