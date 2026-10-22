// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

export default defineConfig({
	site: "https://www.echolocal.link",
	integrations: [
		starlight({
			title: "EchoLocal",
			description: "Turn an Amazon Echo Dot or Echo Show into a local Home Assistant voice satellite, speaker and display.",
			favicon: "/favicon.png",
			customCss: ['./src/styles/custom.css', './src/styles/accent.css'],
			head: [{ tag: 'script', attrs: { src: '/zoom.js', defer: true } }],
			social: [{ icon: 'github', label: 'GitHub', href: "https://github.com/ygelfand/echolocal" }],
			sidebar: [
				{
					"label": "Get started",
					"items": [
						{
							"label": "Supported devices",
							"slug": "start/devices"
						},
						{
							"label": "Before you install",
							"slug": "start/before"
						},
						{
							"label": "Install EchoLocal",
							"slug": "start/install"
						},
						{
							"label": "Add it to Home Assistant",
							"slug": "start/home-assistant"
						},
						{
							"label": "The EchoLocal integration",
							"slug": "start/integration"
						}
					]
				},
				{
					"label": "Echo Dot",
					"items": [
						{
							"label": "Buttons and light ring",
							"slug": "use/dot"
						}
					]
				},
				{
					"label": "Echo Show",
					"items": [
						{
							"label": "Getting around",
							"slug": "use/getting-around"
						},
						{
							"label": "Dashboards",
							"slug": "use/dashboards"
						},
						{
							"label": "Styles and themes",
							"slug": "use/styles"
						},
						{
							"label": "Screen saver",
							"slug": "use/screen-saver"
						},
						{
							"label": "Visuals",
							"slug": "use/visuals"
						}
					]
				},
				{
					"label": "Settings",
					"items": [
						{
							"label": "Where to change settings",
							"slug": "use/configuration"
						},
						{
							"label": "On the screen",
							"slug": "use/settings"
						},
						{
							"label": "In Home Assistant",
							"slug": "use/entities"
						},
						{
							"label": "In a browser",
							"slug": "use/web"
						},
						{
							"label": "All settings and defaults",
							"slug": "use/reference"
						}
					]
				},
				{
					"label": "Features",
					"items": [
						{
							"label": "Music and casting",
							"slug": "features/music"
						},
						{
							"label": "Voice assistants",
							"slug": "features/voice"
						},
						{
							"label": "Microphones",
							"slug": "features/microphones"
						},
						{
							"label": "Intercom",
							"slug": "features/intercom"
						},
						{
							"label": "Messages, timers and alarms",
							"slug": "features/messages"
						},
						{
							"label": "Custom tones",
							"slug": "features/tones"
						},
						{
							"label": "Apps",
							"slug": "features/apps"
						},
						{
							"label": "Photos and wallpaper",
							"slug": "features/photos"
						},
						{
							"label": "Camera",
							"slug": "features/camera"
						},
						{
							"label": "Privacy",
							"slug": "features/privacy"
						},
						{
							"label": "Updates",
							"slug": "features/updates"
						}
					]
				}
			],
		}),
	],
});
