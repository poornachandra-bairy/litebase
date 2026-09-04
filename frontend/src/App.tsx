import { Navigate, Route, Routes } from "react-router-dom";
import { useAuth } from "./lib/auth";
import { Loading } from "./components/common";
import { Layout } from "./components/Layout";
import { LoginPage } from "./pages/Login";
import { DashboardPage } from "./pages/Dashboard";
import { DatabasesPage } from "./pages/Databases";
import { DatabasePage } from "./pages/Database";
import { TablePage } from "./pages/Table";
import { SqlEditorPage } from "./pages/SqlEditor";
import { ApiBuilderPage } from "./pages/ApiBuilder";
import { ApiKeysPage } from "./pages/ApiKeys";
import { BackupsPage } from "./pages/Backups";
import { SettingsPage } from "./pages/Settings";

export function App() {
  const { user, loading } = useAuth();

  // Rendering routes before the session is known would flash the login screen
  // for an already-authenticated operator on every reload.
  if (loading) return <Loading label="Starting Litebase…" />;
  if (!user) return <LoginPage />;

  return (
    <Layout>
      <Routes>
        <Route path="/" element={<DashboardPage />} />
        <Route path="/databases" element={<DatabasesPage />} />
        <Route path="/databases/:db" element={<DatabasePage />} />
        <Route path="/databases/:db/tables/:table" element={<TablePage />} />
        <Route path="/sql" element={<SqlEditorPage />} />
        <Route path="/sql/:db" element={<SqlEditorPage />} />
        <Route path="/api" element={<ApiBuilderPage />} />
        <Route path="/keys" element={<ApiKeysPage />} />
        <Route path="/backups" element={<BackupsPage />} />
        <Route path="/settings" element={<SettingsPage />} />
        {/* Any unknown path returns to the dashboard rather than a dead end. */}
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </Layout>
  );
}
