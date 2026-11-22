import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import '@fontsource-variable/geologica/full.css'
import '@fontsource-variable/martian-mono/wdth.css'
import './styles.css'
import { AuthProvider, useAuth } from './app/auth'
import { Shell } from './app/Shell'
import { OverlayProvider } from './ui/overlay'
import { Loading } from './ui/core'
import { Login } from './pages/Login'
import { Overview } from './pages/Overview'
import { Rulesets } from './pages/Rulesets'
import { RulesetForm } from './pages/RulesetForm'
import { RulesetDetail } from './pages/RulesetDetail'
import { Converter } from './pages/Converter'
import { Audit } from './pages/Audit'
import { Buckets } from './pages/Buckets'
import { BucketBrowser } from './pages/BucketBrowser'
import { Settings } from './pages/Settings'

// The server rewrites <base href> to the panel path; routes hang off it.
const basename = new URL(document.baseURI).pathname.replace(/\/$/, '') || '/'

function App() {
  const { me, ready } = useAuth()
  if (!ready) return <Loading />
  if (!me) return <Login />
  return (
    <Routes>
      <Route element={<Shell />}>
        <Route index element={<Overview />} />
        <Route path="rulesets" element={<Rulesets />} />
        <Route path="rulesets/new" element={<RulesetForm />} />
        <Route path="rulesets/:id" element={<RulesetDetail />} />
        <Route path="rulesets/:id/edit" element={<RulesetForm />} />
        <Route path="convert" element={<Converter />} />
        <Route path="audit" element={<Audit />} />
        <Route path="storage" element={<Buckets />} />
        <Route path="storage/:bucket" element={<BucketBrowser />} />
        <Route path="settings" element={<Settings />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  )
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <BrowserRouter basename={basename}>
      <OverlayProvider>
        <AuthProvider>
          <App />
        </AuthProvider>
      </OverlayProvider>
    </BrowserRouter>
  </StrictMode>,
)
