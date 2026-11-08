// Fonts are bundled locally via @font-face in style.css (latin + latin-ext subsets only).
import './style.css'
import { mount } from 'svelte'
import App from './App.svelte'

const app = mount(App, {
  target: document.getElementById('app')!
})

export default app
